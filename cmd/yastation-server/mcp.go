// MCP support for yastation-server: exposes the same command set as
// the REST endpoints in main.go, but as an MCP server so any
// MCP-speaking AI can use Alice as a tool.
//
// Two ways this gets served, both defined here:
//   - POST/GET /mcp, mounted on the same mux/port as /command — see
//     mcpRoute, wired up from runHTTP in main.go. Same X-Yandex-Token/
//     X-Station headers, same access.json allowlist, same optional
//     default-account fallback as everything else on this server.
//   - `-stdio` (see runStdio, called from main() before runHTTP even
//     starts): a completely separate local mode with no HTTP at all —
//     MCP over stdin/stdout for whichever account is already logged in
//     via cmd/yastation-auth. This is what claude_desktop_config.json's
//     "command"+"args" launches; Claude Desktop doesn't speak to a
//     port, it spawns a subprocess and talks to its stdio.
//
// One caveat baked into every relevant tool's description: Alice's
// spoken reply to a voice command is only ever heard through the
// physical speaker — the underlying (unofficial, reverse-engineered)
// protocol gives no way to capture it as text, see PROTOCOL_NOTES.md
// and internal/quasar. So alice_ask confirms the command was sent, it
// does not — cannot — return what Alice said back.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/dispatch"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runStdio serves one already-authenticated local account over stdio —
// the shape Claude Desktop (and most other local MCP hosts) expect for
// a "command"+"args" entry. All log output goes to stderr (Go's log
// package default), keeping stdout clean for MCP JSON-RPC. No HTTP,
// no access.json — it's just you, locally.
func runStdio(defaultsCfg, customCfg *app.CustomCommandConfig) {
	client, err := quasar.Connect()
	if err != nil {
		log.Fatalf("Не смог подключиться к сохранённому аккаунту (запусти сперва: go run ./cmd/yastation-auth): %v", err)
	}
	log.Printf("Подключено (stdio). Колонок найдено: %d", len(client.Speakers))

	// quasar.Connect() already applies YASTATION_STATION_ID/NAME to
	// client.DefaultDeviceID/Name (same env vars the REPL/HTTP default-
	// account mode use) — leave defaultStation empty here so say/ask
	// fall through to that instead of a second, redundant mechanism.
	server := buildMCPServer(client, "", defaultsCfg, customCfg)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("stdio-сервер завершился с ошибкой: %v", err)
	}
}

// mcpRoute builds the http.Handler for /mcp: the SDK's streamable-HTTP
// handler wrapped in account resolution. defaultClient may be nil (pure
// BYOT server) — see mcpAccountMiddleware.
func mcpRoute(defaultClient *quasar.Client, tokenClients *tokenClientCache, defaultsCfg, customCfg *app.CustomCommandConfig, loadAccess func() *access.List) http.Handler {
	getServer := func(r *http.Request) *mcp.Server {
		client, ok := r.Context().Value(mcpClientCtxKey{}).(*quasar.Client)
		if !ok || client == nil {
			// Shouldn't happen — mcpAccountMiddleware already gated this
			// request — but bail out cleanly instead of panicking on a
			// nil client if it somehow does.
			return nil
		}
		return buildMCPServer(client, r.Header.Get("X-Station"), defaultsCfg, customCfg)
	}
	// DisableLocalhostProtection: the SDK's DNS-rebinding guard rejects any
	// request that arrives via loopback with a non-localhost Host header.
	// Behind a reverse proxy (Caddy/nginx → localhost:port) every request is
	// loopback with the real domain as Host, so that guard would 403 all of
	// them. Safe here because mcpAccountMiddleware has already required a
	// valid allowlisted X-Yandex-Token before any of this runs — the
	// protection would only matter for an unauthenticated server.
	return mcpAccountMiddleware(defaultClient, tokenClients, loadAccess, mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
}

// mcpClientCtxKey is the context key used to hand the already-resolved
// *quasar.Client from mcpAccountMiddleware down to mcpRoute's getServer,
// so the token is only ever looked up/validated once per request.
type mcpClientCtxKey struct{}

// mcpAccountMiddleware picks whose Yandex account an /mcp request runs
// against — the exact same rule runOnAccount (main.go) uses for
// /command: caller's own X-Yandex-Token if present (checked against the
// allowlist), else the server's default account if it has one, else a
// rejection. Runs *before* the MCP layer, so auth failures come back as
// a plain JSON HTTP error instead of an opaque MCP protocol failure.
//
// Deliberately always 403, never 401: per the MCP Authorization spec
// (RFC 9728), a 401 response is a signal to MCP clients that they
// should attempt OAuth discovery (WWW-Authenticate / .well-known
// probing, then a browser redirect to an /authorize endpoint we don't
// have). We're not an OAuth server — X-Yandex-Token is a plain static
// credential the caller is expected to already have and set as a
// header, same as the REST endpoints. 403 ("access denied") doesn't
// carry that OAuth-retry connotation under RFC 9110, so spec-compliant
// clients treat it as a plain rejection instead of a cue to redirect.
func mcpAccountMiddleware(defaultClient *quasar.Client, tokenClients *tokenClientCache, loadAccess func() *access.List, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xToken := r.Header.Get("X-Yandex-Token")
		if xToken == "" {
			if defaultClient == nil {
				writeMCPAuthError(w, r, "нужен заголовок X-Yandex-Token (сервер запущен без своего аккаунта — BYOT по умолчанию). "+reauthHint(r))
				return
			}
			ctx := context.WithValue(r.Context(), mcpClientCtxKey{}, defaultClient)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		client, _, err := tokenClients.get(xToken, loadAccess())
		if err != nil {
			writeMCPAuthError(w, r, err.Error()+". "+reauthHint(r))
			return
		}
		ctx := context.WithValue(r.Context(), mcpClientCtxKey{}, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authURL reconstructs the server's /auth/start address as the caller
// sees it — the browser-facing re-auth entry point (see webauth.go), which
// hands back a fresh x-token to put into the X-Yandex-Token header. It's
// derived from the request rather than hardcoded so it survives being
// behind a reverse proxy: Caddy forwards the real domain as Host and the
// proto as X-Forwarded-Proto, and that's exactly the URL the user should
// open. "https" is the fallback proto because in production this server
// only ever answers behind the proxy; the loopback host fallback only
// matters for tests/plain local curl.
func authURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "https"
	} else if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	host := r.Host
	if host == "" {
		host = "localhost:8737"
	}
	return proto + "://" + host + "/auth/start"
}

// reauthHint is deliberately in English too — MCP client UIs/logs that
// surface this text tend to render it verbatim, and a short unambiguous
// instruction beats a client guessing an OAuth flow on its own.
func reauthHint(r *http.Request) string {
	url := authURL(r)
	return "Токен недействителен или истёк. Получи новый: " + url + ". / Token invalid or expired: get a fresh one at " + url + "."
}

// writeMCPAuthError always answers 403 (see mcpAccountMiddleware for
// why) and deliberately never sets WWW-Authenticate — that header is
// specifically what tells spec-compliant MCP clients "start OAuth
// discovery here", which is the exact behaviour we're avoiding. The body
// carries the /auth/start link as a separate "auth_url" field so a client
// that can't parse the message text still gets the URL.
func writeMCPAuthError(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, "{%q: %q, %q: %q}\n", "error", msg, "auth_url", authURL(r))
}

// --- MCP tools --------------------------------------------------------

type emptyParams struct{}

// buildMCPServer wires up one *mcp.Server bound to a single Yandex
// account (client) and a default station, with every dispatcher command
// that has a bound handler (see internal/dispatch.Dispatcher.Specs)
// exposed as its own MCP tool with its own named, typed parameters —
// generated straight from the dispatcher (addSpecTool), so a tool's
// schema can never drift out of sync with what the command actually
// accepts. There's no generic "run this line" tool anymore: same
// reasoning as cmd/yastation-server's POST /commands/{name} (see
// main.go's package doc comment) — slash-line parsing is a REPL-only
// concern, never something an MCP tool call replays.
func buildMCPServer(client *quasar.Client, defaultStation string, defaultsCfg, customCfg *app.CustomCommandConfig) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "yastation",
		Version: "0.2.0",
	}, nil)

	// Specs are static — identical no matter which account eventually
	// runs a command — so a throwaway App with a nil Client is enough to
	// read them off (same trick main.go's handleCommandByName/
	// handleCommandsList use); it never calls a handler, the only thing
	// that would ever touch Client.
	specs := buildApp(nil, defaultsCfg, customCfg).Dispatcher.Specs()
	for _, spec := range specs {
		addSpecTool(server, spec, client, defaultStation, defaultsCfg, customCfg)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "alice_help",
		Description: "Обзор всех команд yastation одним текстом, по категориям — то же самое, что /help в REPL. Обычно не нужен: каждая команда уже своим отдельным инструментом (alice_say, alice_volume, ...) со своим описанием и параметрами; загляни сюда, если нужен общий обзор сразу всего.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *emptyParams) (*mcp.CallToolResult, any, error) {
		a := buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
		return textResult(a.Dispatcher.Help()), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "alice_stations",
		Description: "Список колонок (станций), доступных на этом Яндекс-аккаунте — id, имя, дом.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *emptyParams) (*mcp.CallToolResult, any, error) {
		if len(client.Speakers) == 0 {
			return textResult("колонок не найдено"), nil, nil
		}
		var b strings.Builder
		for _, d := range client.Speakers {
			fmt.Fprintf(&b, "- %s (id=%s", d.Name, d.ID)
			if d.HouseName != "" {
				fmt.Fprintf(&b, ", дом %q", d.HouseName)
			}
			b.WriteString(")\n")
		}
		return textResult(b.String()), nil, nil
	})

	return server
}

// addSpecTool registers one MCP tool for spec — named "alice_<command>",
// with an input schema built directly from spec.Params (every value a
// plain JSON string, same convention internal/dispatch.BoundHandler and
// cmd/yastation-server's POST /commands/{name} already use; a "number"
// Kind is a description hint only, not a JSON Schema type, since the
// value still arrives as a string either way — see dispatch.Param). The
// handler itself is a thin shim: decode arguments, hand them straight to
// ExecuteNamed, no slash-line anywhere in between.
func addSpecTool(server *mcp.Server, spec dispatch.CommandSpec, client *quasar.Client, defaultStation string, defaultsCfg, customCfg *app.CustomCommandConfig) {
	properties := map[string]any{}
	var required []string
	if spec.TakesStation {
		properties["station"] = map[string]any{
			"type":        "string",
			"description": "Имя колонки; если не указано, используется станция по умолчанию для этого MCP-подключения.",
		}
	}
	for _, p := range spec.Params {
		desc := p.Help
		if p.Kind == "number" {
			if desc != "" {
				desc += " "
			}
			desc += "(число, JSON-строкой)"
		}
		properties[p.Name] = map[string]any{"type": "string", "description": desc}
		if !p.Optional {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}

	description := spec.Help
	if spec.Name == "cmd" {
		description += " ВАЖНО: протокол Яндекса не даёт способа получить текстовый ответ Алисы обратно — он прозвучит только из динамика колонки. Этот инструмент лишь подтверждает, что команда отправлена, он не возвращает, что именно ответила Алиса."
	}

	server.AddTool(&mcp.Tool{
		Name:        "alice_" + spec.Name,
		Description: description,
		InputSchema: schema,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := map[string]json.RawMessage{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &body); err != nil {
				return errResult(fmt.Errorf("невалидные аргументы: %w", err)), nil
			}
		}
		station, values, err := valuesFromBody(spec, body)
		if err != nil {
			return errResult(err), nil
		}

		a := buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
		out, ok, err := a.ExecuteNamed(ctx, spec.Name, pick(station, defaultStation), values)
		if err != nil {
			return errResult(err), nil
		}
		if !ok {
			return errResult(fmt.Errorf("команда не найдена: %s", spec.Name)), nil
		}
		return textResult(out), nil
	})
}

func pick(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// errResult turns err into a CallToolResult with IsError set, the shape
// server.AddTool (the low-level API addSpecTool uses, so it can build
// each tool's schema dynamically from a dispatch.CommandSpec) expects
// for a failed call — the generic top-level mcp.AddTool wrapper used
// elsewhere in this file does this same wrapping automatically for a
// non-nil returned error, but the low-level API leaves it to the caller.
func errResult(err error) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	r.SetError(err)
	return r
}

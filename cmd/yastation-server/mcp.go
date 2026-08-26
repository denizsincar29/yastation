// MCP support for yastation-server: exposes the same command set as
// the REST endpoints in main.go, but as an MCP server so any
// MCP-speaking AI can use Alice as a tool.
//
// Two ways this gets served, both defined here:
//   - POST/GET /mcp, mounted on the same mux/port as /command — see
//     mcpRoute, wired up from runHTTP in main.go. Same X-Yandex-Token/
//     X-Station headers, same access.json allowlist, same optional
//     default-account fallback, same YASTATION_HTTP_TOKEN gate as
//     everything else on this server.
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
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runStdio serves one already-authenticated local account over stdio —
// the shape Claude Desktop (and most other local MCP hosts) expect for
// a "command"+"args" entry. All log output goes to stderr (Go's log
// package default), keeping stdout clean for MCP JSON-RPC. No HTTP,
// no YASTATION_HTTP_TOKEN, no access.json — it's just you, locally.
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
	return mcpAccountMiddleware(defaultClient, tokenClients, loadAccess, mcp.NewStreamableHTTPHandler(getServer, nil))
}

// mcpClientCtxKey is the context key used to hand the already-resolved
// *quasar.Client from mcpAccountMiddleware down to mcpRoute's getServer,
// so the token is only ever looked up/validated once per request.
type mcpClientCtxKey struct{}

// mcpAccountMiddleware picks whose Yandex account an /mcp request runs
// against — the exact same rule runOnAccount (main.go) uses for
// /command: caller's own X-Yandex-Token if present (checked against the
// allowlist), else the server's default account if it has one, else a
// plain 401. Runs *before* the MCP layer, so auth failures come back as
// a plain JSON HTTP error instead of an opaque MCP protocol failure.
func mcpAccountMiddleware(defaultClient *quasar.Client, tokenClients *tokenClientCache, loadAccess func() *access.List, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xToken := r.Header.Get("X-Yandex-Token")
		if xToken == "" {
			if defaultClient == nil {
				writeMCPAuthError(w, http.StatusUnauthorized,
					"нужен заголовок X-Yandex-Token (сервер запущен без своего аккаунта — BYOT по умолчанию)")
				return
			}
			ctx := context.WithValue(r.Context(), mcpClientCtxKey{}, defaultClient)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		client, _, err := tokenClients.get(xToken, loadAccess())
		if err != nil {
			writeMCPAuthError(w, statusForTokenError(err), err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), mcpClientCtxKey{}, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeMCPAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "{%q: %q}\n", "error", msg)
}

// --- MCP tools --------------------------------------------------------

type sayParams struct {
	Text    string `json:"text" jsonschema:"Текст для озвучивания через колонку (TTS)"`
	Station string `json:"station,omitempty" jsonschema:"Имя колонки; если не указано, используется станция по умолчанию (заголовок X-Station)"`
}

type askParams struct {
	Text    string `json:"text" jsonschema:"Голосовая команда/вопрос для Алисы, например \"какая погода\" или \"включи радио на кухне\""`
	Station string `json:"station,omitempty" jsonschema:"Имя колонки; если не указано, используется станция по умолчанию (заголовок X-Station)"`
}

type mcpCommandParams struct {
	Line string `json:"line" jsonschema:"Полная команда так, как она вводится в REPL yastation, например \"/timer 10 варка яиц\", \"/volume 5\", \"/scenario Вечер\" или \"- какая погода\". Список команд — через alice_help"`
}

type emptyParams struct{}

// buildMCPServer wires up one *mcp.Server bound to a single Yandex
// account (client) and a default station, with every yastation
// dispatcher command reachable either directly (alice_say/alice_ask)
// or through the alice_command escape hatch.
func buildMCPServer(client *quasar.Client, defaultStation string, defaultsCfg, customCfg *app.CustomCommandConfig) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "yastation",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "alice_say",
		Description: "Сказать текст через Яндекс Станцию (TTS) — Алиса просто произносит текст вслух, без интерпретации как команды.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *sayParams) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(p.Text) == "" {
			return nil, nil, fmt.Errorf("text не может быть пустым")
		}
		a := buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
		out, err := a.ExecuteArgs(ctx, "say", stationArgv(pick(p.Station, defaultStation), p.Text))
		if err != nil {
			return nil, nil, err
		}
		return textResult(out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "alice_ask",
		Description: "Отправить Алисе голосовую команду/вопрос — как если бы её произнесли вслух в колонку. " +
			"ВАЖНО: протокол Яндекса не даёт способа получить текстовый ответ Алисы обратно — он прозвучит только из динамика колонки. " +
			"Этот инструмент лишь подтверждает, что команда отправлена, он не возвращает, что именно ответила Алиса.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *askParams) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(p.Text) == "" {
			return nil, nil, fmt.Errorf("text не может быть пустым")
		}
		a := buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
		out, err := a.ExecuteArgs(ctx, "cmd", stationArgv(pick(p.Station, defaultStation), p.Text))
		if err != nil {
			return nil, nil, err
		}
		return textResult(out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "alice_command",
		Description: "Выполнить произвольную команду yastation одной строкой — эскейп-хэтч на все команды сразу (таймеры, будильники, сценарии, громкость, плеер, свои команды из commands.json и т.д). Синтаксис и список команд — через alice_help.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *mcpCommandParams) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(p.Line) == "" {
			return nil, nil, fmt.Errorf("line не может быть пустым")
		}
		a := buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
		out, err := a.Execute(ctx, p.Line)
		if err != nil {
			return nil, nil, err
		}
		return textResult(out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "alice_help",
		Description: "Список всех доступных команд yastation с описанием и синтаксисом каждой — то же самое, что /help в REPL. Вызови перед alice_command, если не уверен в синтаксисе.",
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

// stationArgv builds the []string argv that say/cmd handlers expect:
// an optional leading "station=Name" token (see internal/app.station)
// followed by the whole text as a single element, so dispatch.Rest
// reconstructs it byte-for-byte instead of collapsing whitespace.
func stationArgv(station, text string) []string {
	if station == "" {
		return []string{text}
	}
	return []string{"station=" + station, text}
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

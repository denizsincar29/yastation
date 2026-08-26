// Command yastation-mcp exposes yastation as a remote MCP server (over
// the streamable-HTTP transport), so any MCP-speaking AI — Claude,
// or anything else that can add a remote MCP connector — can talk to
// Alice: say phrases, send voice commands, run any dispatcher command
// yastation knows about.
//
// Auth mirrors yastation-server's bring-your-own-token model exactly,
// and reuses the very same access.json allowlist (see internal/access,
// cmd/yastation-access) — add someone there once and they're allowed
// through both the HTTP backend and this MCP server:
//
//   - X-Yandex-Token (required): the caller's own Yandex OAuth token.
//     Checked against the allowlist on *every* request, not just at
//     connect time, so revoking someone (yastation-access remove)
//     takes effect on their very next request rather than after some
//     cache expires.
//   - X-Station (optional): default station name/id every tool falls
//     back to when its own "station" argument is omitted. Same header
//     yastation-server uses.
//
// A missing or disallowed X-Yandex-Token gets a plain 401/403 with a
// JSON body *before* the request ever reaches the MCP layer — an AI
// misconfigured without the header sees a clear HTTP error, not a
// mysterious protocol failure.
//
// One caveat baked into every relevant tool's description: Alice's
// spoken reply to a voice command is only ever heard through the
// physical speaker — the underlying (unofficial, reverse-engineered)
// protocol gives no way to capture it as text, see PROTOCOL_NOTES.md
// and internal/quasar. So alice_ask confirms the command was sent, it
// does not — cannot — return what Alice said back.
//
// Two ways to run it:
//
//   - Default (HTTP): the multi-account server described above, meant
//     to sit behind a reverse proxy so several people/AIs can each use
//     their own X-Yandex-Token.
//   - `-stdio`: a local single-account server for Claude Desktop's
//     claude_desktop_config.json ("command"+"args", launched as a
//     subprocess talking MCP over stdin/stdout — no networking, no
//     headers). Uses whatever account is already logged in via
//     cmd/yastation-auth (same tokens.json the REPL uses), so there's
//     nothing Yandex-related to put in the json config at all.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	stdio := flag.Bool("stdio", false, "запустить как локальный stdio MCP-сервер (для claude_desktop_config.json) вместо HTTP")
	flag.Parse()

	defaultsCfg, customCfg := loadCommandConfigs()

	if *stdio {
		runStdio(defaultsCfg, customCfg)
		return
	}
	runHTTP(defaultsCfg, customCfg)
}

// loadCommandConfigs loads the standard config.json.default plus an
// optional custom commands file (YASTATION_COMMANDS_FILE) — shared by
// both the HTTP and stdio entry points.
func loadCommandConfigs() (defaultsCfg, customCfg *app.CustomCommandConfig) {
	defaultsPath := app.ConfigFilePath()
	if err := app.EnsureConfigFile(defaultsPath); err != nil {
		log.Fatalf("Не смог создать %s: %v", defaultsPath, err)
	}
	defaultsCfg, err := app.LoadCustomCommandConfig(defaultsPath)
	if err != nil {
		log.Fatalf("Не смог загрузить %s: %v", defaultsPath, err)
	}
	log.Printf("Загружено стандартных команд: %d (из %s)", len(defaultsCfg.Commands), defaultsPath)

	if p := os.Getenv("YASTATION_COMMANDS_FILE"); p != "" {
		cfg, err := app.LoadCustomCommandConfig(p)
		if err != nil {
			log.Fatalf("Не смог загрузить свои команды из %s: %v", p, err)
		}
		customCfg = cfg
		log.Printf("Загружено своих команд: %d (из %s)", len(cfg.Commands), p)
	}
	return defaultsCfg, customCfg
}

// runStdio serves one already-authenticated local account over stdio —
// the shape Claude Desktop (and most other local MCP hosts) expect for
// a "command"+"args" entry. All log output goes to stderr (Go's log
// package default), keeping stdout clean for MCP JSON-RPC.
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

func runHTTP(defaultsCfg, customCfg *app.CustomCommandConfig) {
	addr := envOr("YASTATION_MCP_ADDR", ":8738")

	accessPath := access.FilePath()
	loadAccess := func() *access.List {
		l, err := access.Load(accessPath)
		if err != nil {
			log.Printf("не смог прочитать %s: %v — считаю список допуска пустым (никому не разрешён доступ)", accessPath, err)
			return &access.List{}
		}
		return l
	}
	if initial := loadAccess(); len(initial.Entries) == 0 {
		log.Printf("Список допуска %s пуст — MCP-серверу сейчас никто не разрешён.", accessPath)
		log.Println("Добавь кого-нибудь (тот же список, что и у yastation-server): go run ./cmd/yastation-access add")
	} else {
		log.Printf("В списке допуска %d аккаунт(ов) (%s)", len(initial.Entries), accessPath)
	}

	tokenClients := newTokenClientCache(20 * time.Minute)

	getServer := func(r *http.Request) *mcp.Server {
		client, ok := r.Context().Value(clientCtxKey{}).(*quasar.Client)
		if !ok || client == nil {
			// Shouldn't happen — withYandexAuth already gated this
			// request — but bail out cleanly instead of panicking on
			// a nil client if it somehow does.
			return nil
		}
		return buildMCPServer(client, r.Header.Get("X-Station"), defaultsCfg, customCfg)
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(getServer, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/mcp", withYandexAuth(tokenClients, loadAccess, mcpHandler))

	log.Println("MCP-сервер yastation слушает на", addr, "(эндпоинт /mcp)")
	log.Println("Заголовки: X-Yandex-Token (обязателен, свой OAuth-токен) и X-Station (необязателен, станция по умолчанию)")
	log.Fatal(http.ListenAndServe(addr, mux))
}

func buildApp(client *quasar.Client, defaultsCfg, customCfg *app.CustomCommandConfig) *app.App {
	a := app.New(client)
	if defaultsCfg != nil {
		if err := a.RegisterCustomCommands(defaultsCfg); err != nil {
			log.Fatalf("Ошибка в конфиге стандартных команд: %v", err)
		}
	}
	if customCfg != nil {
		if err := a.RegisterCustomCommands(customCfg); err != nil {
			log.Fatalf("Ошибка в конфиге команд: %v", err)
		}
	}
	return a
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "ok")
}

// --- per-token client cache (bring-your-own-token mode) -----------------
//
// Deliberately mirrors cmd/yastation-server's tokenClientCache — same
// hashing, same TTL/sweep logic, same allowlist re-check on every hit —
// so the two servers behave identically for the same access.json. Kept
// as its own small copy rather than factored into a shared package to
// avoid disturbing yastation-server's existing package-private tests.

type tokenClientCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*tokenCacheEntry

	resolveIdentity func(xToken string) (quasar.Identity, error)
	buildClient     func(xToken string) (*quasar.Client, error)
}

type tokenCacheEntry struct {
	client   *quasar.Client
	identity quasar.Identity
	expires  time.Time
}

func newTokenClientCache(ttl time.Duration) *tokenClientCache {
	return &tokenClientCache{
		ttl:             ttl,
		entries:         map[string]*tokenCacheEntry{},
		resolveIdentity: quasar.WhoAmIFromXToken,
		buildClient:     quasar.ClientFromXToken,
	}
}

// hashToken never stores or logs the raw token, only a digest.
func hashToken(xToken string) string {
	sum := sha256.Sum256([]byte(xToken))
	return hex.EncodeToString(sum[:])
}

// notAllowedError means the token was valid and its account resolved
// fine, but that account's uid isn't on the access.json allowlist.
type notAllowedError struct{ id quasar.Identity }

func (e *notAllowedError) Error() string {
	label := e.id.RealName
	if label == "" {
		label = e.id.Login
	}
	if label == "" {
		label = e.id.UID
	}
	return fmt.Sprintf("аккаунт %q не в списке допуска (uid=%s) — попроси владельца сервера: yastation-access add", label, e.id.UID)
}

func (c *tokenClientCache) get(xToken string, list *access.List) (*quasar.Client, quasar.Identity, error) {
	key := hashToken(xToken)

	c.mu.Lock()
	c.sweepLocked()
	if e, ok := c.entries[key]; ok {
		e.expires = time.Now().Add(c.ttl)
		client, id := e.client, e.identity
		c.mu.Unlock()
		if !list.IsAllowed(id.UID) {
			return nil, id, &notAllowedError{id}
		}
		return client, id, nil
	}
	c.mu.Unlock()

	id, err := c.resolveIdentity(xToken)
	if err != nil {
		return nil, quasar.Identity{}, fmt.Errorf("не удалось определить аккаунт по токену: %w", err)
	}
	if !list.IsAllowed(id.UID) {
		return nil, id, &notAllowedError{id}
	}

	client, err := c.buildClient(xToken)
	if err != nil {
		return nil, id, err
	}

	c.mu.Lock()
	c.entries[key] = &tokenCacheEntry{client: client, identity: id, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return client, id, nil
}

func (c *tokenClientCache) sweepLocked() {
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}

func statusForTokenError(err error) int {
	var na *notAllowedError
	if errors.As(err, &na) {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}

// --- auth middleware ------------------------------------------------

// clientCtxKey is the context key used to hand the already-resolved
// *quasar.Client from withYandexAuth down to getServer, so the token
// is only ever looked up/validated once per request.
type clientCtxKey struct{}

// withYandexAuth requires a valid, allowlisted X-Yandex-Token on every
// request and stashes the resolved client in the request context.
// Runs *before* the MCP layer, so auth failures come back as a plain
// JSON HTTP error instead of an opaque MCP protocol failure.
func withYandexAuth(tokenClients *tokenClientCache, loadAccess func() *access.List, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xToken := r.Header.Get("X-Yandex-Token")
		if xToken == "" {
			writeAuthError(w, http.StatusUnauthorized, "нужен заголовок X-Yandex-Token со своим Яндекс OAuth-токеном")
			return
		}
		client, _, err := tokenClients.get(xToken, loadAccess())
		if err != nil {
			writeAuthError(w, statusForTokenError(err), err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), clientCtxKey{}, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
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

type commandParams struct {
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, p *commandParams) (*mcp.CallToolResult, any, error) {
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

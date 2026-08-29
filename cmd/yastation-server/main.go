// Command yastation-server exposes the same command set as the REPL over
// HTTP, so you can control the station from any program/script/curl call
// on your server, not just an interactive terminal. Every request is
// funnelled through the same single-worker queue as everything else, so
// concurrent requests can't race each other editing the same speaker's
// scenario; each request waits for its own actual result before
// answering (see internal/app.App.Execute).
//
// Two ways to send a command:
//   - POST /command — the simple TTS/voice-command convenience form:
//     {"text": "...", "station": "...", "as_command": false}.
//   - POST /commands/{name} — one auto-registered URL per dispatcher
//     command (GET /commands lists them all, with each command's exact
//     JSON field names/types) — built-ins (say, volume, scenario, ...)
//     and config-driven commands (config.json.default,
//     --config/commands.json) alike take their declared param names
//     directly as JSON fields — see handleCommandByName's doc comment
//     for the exact shape.
//
// Neither endpoint builds or parses a "/name ..." slash-line — that
// tokenizing stays a REPL-only concern (see internal/dispatch's Execute
// vs CallNamed). Same story for MCP: every tool in mcp.go takes its own
// named, typed parameters instead of a single line-based escape hatch.
//
// Auth:
//   - "Bring your own token" (default): a request carrying an
//     X-Yandex-Token header runs against *that* Yandex account — but
//     only if that account's uid is on the allowlist (see
//     internal/access, cmd/yastation-access). No account of the server's
//     own is needed — nothing to authorize on the box, nothing to leak
//     if the box is compromised. Clients built this way are cached
//     briefly per token (see tokenClientCache) so repeated requests
//     don't redo the login handshake every time, but the allowlist is
//     re-checked on every request even against a cached client, so
//     revoking someone (yastation-access remove) takes effect on their
//     very next request, not after the cache entry expires.
//   - Own account (opt-in via YASTATION_USE_DEFAULT_ACCOUNT=1): the
//     server also keeps its own pre-authenticated account (from
//     yastation-auth) and uses it for any request that doesn't carry
//     X-Yandex-Token. Not allowlist-gated — it's the server's own
//     account, trusted by definition. This mode answers requests from
//     anyone who can reach the server, so keep it behind a firewall/VPN
//     or a reverse proxy with its own access control.
//
// Every request must therefore carry a valid X-Yandex-Token (with the
// exception of the /auth/* browser flow) or fall through to the default
// account if one is configured. There is no separate HTTP API key: the
// allowlist is what stops random callers from driving the server.
//
// Besides the REST endpoints above, POST/GET /mcp on the same port
// speaks MCP (Streamable HTTP) — same X-Yandex-Token/X-Station headers,
// same allowlist, same optional default-account fallback; see mcp.go.
// One process, one port, no separate server to run just for MCP.
//
// `-stdio` is a third, unrelated mode: skips HTTP entirely and serves
// MCP over stdin/stdout for a single already-authenticated local
// account (the one from cmd/yastation-auth) — the shape Claude Desktop
// and friends expect from claude_desktop_config.json's "command"+"args".
// See mcp.go's runStdio.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/denizsincar29/yastation/internal/dispatch"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	stdio := flag.Bool("stdio", false, "запустить как локальный stdio MCP-сервер (Claude Desktop и т.п.) вместо HTTP")
	flag.Parse()

	defaultsPath := app.ConfigFilePath()
	if err := app.EnsureConfigFile(defaultsPath); err != nil {
		log.Fatalf("Не смог создать %s: %v", defaultsPath, err)
	}
	defaultsCfg, err := app.LoadCustomCommandConfig(defaultsPath)
	if err != nil {
		log.Fatalf("Не смог загрузить %s: %v", defaultsPath, err)
	}
	log.Printf("Загружено стандартных команд: %d (из %s)", len(defaultsCfg.Commands), defaultsPath)

	var customCfg *app.CustomCommandConfig
	if p := os.Getenv("YASTATION_COMMANDS_FILE"); p != "" {
		cfg, err := app.LoadCustomCommandConfig(p)
		if err != nil {
			log.Fatalf("Не смог загрузить свои команды из %s: %v", p, err)
		}
		customCfg = cfg
		log.Printf("Загружено своих команд: %d (из %s)", len(cfg.Commands), p)
	}

	if *stdio {
		runStdio(defaultsCfg, customCfg)
		return
	}
	runHTTP(defaultsCfg, customCfg)
}

func runHTTP(defaultsCfg, customCfg *app.CustomCommandConfig) {
	addr := envOr("YASTATION_HTTP_ADDR", ":8737")

	if legacy := os.Getenv("YASTATION_BYOT_ONLY"); legacy != "" {
		log.Println("YASTATION_BYOT_ONLY больше не нужен — сервер теперь и так BYOT по умолчанию.")
		log.Println("Чтобы включить свой аккаунт, задайте YASTATION_USE_DEFAULT_ACCOUNT=1.")
	}

	accessPath := access.FilePath()
	loadAccess := func() *access.List {
		l, err := access.Load(accessPath)
		if err != nil {
			log.Printf("не смог прочитать %s: %v — считаю список допуска пустым (BYOT никому не разрешён)", accessPath, err)
			return &access.List{}
		}
		return l
	}
	if initial := loadAccess(); len(initial.Entries) == 0 {
		log.Printf("Список допуска %s пуст — BYOT сейчас никому не разрешён.", accessPath)
		log.Println("Добавь кого-нибудь: go run ./cmd/yastation-access add")
	} else {
		log.Printf("В списке допуска %d аккаунт(ов) (%s)", len(initial.Entries), accessPath)
	}

	// useDefaultAccount: opt-in only. By default the server has no
	// Yandex account of its own — every request must bring its own
	// X-Yandex-Token. Set this to also keep a default account (from
	// yastation-auth) for requests that don't carry one — used by both
	// the REST endpoints below (see runOnAccount) and /mcp (see
	// mcpAccountMiddleware in mcp.go).
	useDefaultAccount := os.Getenv("YASTATION_USE_DEFAULT_ACCOUNT") != ""

	var defaultApp *app.App
	var defaultClient *quasar.Client
	if !useDefaultAccount {
		log.Println("BYOT (по умолчанию): своего аккаунта нет, каждый запрос должен нести X-Yandex-Token с аккаунта из списка допуска")
	} else {
		log.Println("Подключаюсь к Яндекс Станции (свой аккаунт, YASTATION_USE_DEFAULT_ACCOUNT=1)...")
		client, err := quasar.Connect()
		if err != nil {
			log.Fatalf("Не удалось подключиться: %v\n"+
				"Если это первый запуск — авторизуйтесь: go run ./cmd/yastation-auth", err)
		}
		names := make([]string, len(client.Speakers))
		for i, d := range client.Speakers {
			names[i] = d.Name
		}
		log.Printf("Подключено. Колонок найдено: %d (%s)", len(client.Speakers), strings.Join(names, ", "))
		defaultClient = client
		defaultApp = buildApp(client, defaultsCfg, customCfg)
		defer defaultApp.Close()
	}

	tokenClients := newTokenClientCache(20 * time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("POST /command", handleCommand(defaultApp, tokenClients, defaultsCfg, customCfg, loadAccess))
	mux.HandleFunc("GET /commands", handleCommandsList(defaultsCfg, customCfg))
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(defaultsCfg, customCfg, defaultApp, tokenClients, loadAccess))
	mux.HandleFunc("GET /schedules", handleSchedules(defaultApp))
	mux.HandleFunc("GET /stations", handleStations(tokenClients, loadAccess))
	mux.Handle("/mcp", mcpRoute(defaultClient, tokenClients, defaultsCfg, customCfg, loadAccess))

	authStore := newPendingAuthStore(10 * time.Minute)
	mux.HandleFunc("GET /auth/start", handleAuthStart(authStore))
	mux.HandleFunc("GET /auth/result", handleAuthResult(authStore))

	handler := withLogging(mux)

	log.Println("Слушаю на", addr, "— REST: /command, /commands; MCP (Streamable HTTP): /mcp; браузер: /auth/start")
	log.Fatal(http.ListenAndServe(addr, handler))
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

// --- per-token client cache (bring-your-own-token mode) -----------------

type tokenClientCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*tokenCacheEntry

	// resolveIdentity/buildClient are overridable so tests can exercise
	// get()'s caching/ACL logic without hitting the real Yandex API.
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

// hashToken never stores or logs the raw token, only a digest, so it
// can't leak through a crash dump, log line, or map key inspection.
func hashToken(xToken string) string {
	sum := sha256.Sum256([]byte(xToken))
	return hex.EncodeToString(sum[:])
}

// notAllowedError means the token was valid and its account resolved
// fine, but that account's uid isn't on the access.json allowlist —
// distinct from a plain login failure so handlers can answer 403
// (forbidden) instead of 401 (unauthorized).
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

// get returns a ready quasar.Client for xToken, if its account's uid is
// on list. Building one (identity check + login handshake + device
// refresh) only happens on first use; after that the client is cached
// for the cache's TTL — but list is re-checked on *every* call, cache
// hit or not, so revoking someone takes effect on their very next
// request rather than waiting out the cache.
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

// sweepLocked drops expired entries. Called with c.mu held.
func (c *tokenClientCache) sweepLocked() {
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}

// --- middleware -------------------------------------------------------

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// --- handlers -----------------------------------------------------------

// commandRequest is POST /command's body — the simple TTS/voice-command
// convenience form. For anything else (timers, scenarios, volume, your
// own commands.json commands, ...), use POST /commands/{name} instead
// (see handleCommandByName) — one URL per command with its own named
// JSON fields, generated from the dispatcher itself (GET /commands).
//
// There is deliberately no "line" field here anymore: building a
// "/name ..." string by hand and sending it through the REPL's
// slash-tokenizer was the old shape of this endpoint, and it's the one
// thing every other endpoint in this file (and every MCP tool in mcp.go)
// was reworked specifically to avoid — slash-line parsing is a REPL-only
// concern now (see internal/dispatch.Dispatcher.CallNamed).
type commandRequest struct {
	// Text + Station are the common case: send
	// {"text": "привет", "station": "Кухня"} and it's spoken as-is.
	// Station is optional; omitted means the default speaker. Station
	// can also be given as the X-Station header instead of in the body.
	Text    string `json:"text"`
	Station string `json:"station"`
	// AsCommand sends Text as a voice command (/cmd) instead of TTS
	// (/say).
	AsCommand bool `json:"as_command"`
}

type commandResponse struct {
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func handleCommand(defaultApp *app.App, tokenClients *tokenClientCache, defaultsCfg, customCfg *app.CustomCommandConfig, loadAccess func() *access.List) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req commandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "невалидный JSON: " + err.Error()})
			return
		}
		if station := r.Header.Get("X-Station"); station != "" && req.Station == "" {
			req.Station = station
		}
		if req.Text == "" {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужно поле text"})
			return
		}

		name := "say"
		if req.AsCommand {
			name = "cmd"
		}
		values := map[string]string{"text": req.Text}

		runOnAccount(w, r, defaultApp, tokenClients, defaultsCfg, customCfg, loadAccess,
			func(ctx context.Context, a *app.App) (string, error) {
				out, _, err := a.ExecuteNamed(ctx, name, req.Station, values)
				return out, err
			})
	}
}

// runOnAccount is the auth/account-selection logic shared by every
// endpoint that actually runs a command: bring-your-own-token (checked
// against the allowlist) if X-Yandex-Token is present, otherwise the
// server's own default account if it has one. run does the actual work
// once the right *app.App has been picked.
func runOnAccount(w http.ResponseWriter, r *http.Request, defaultApp *app.App, tokenClients *tokenClientCache, defaultsCfg, customCfg *app.CustomCommandConfig, loadAccess func() *access.List, run func(ctx context.Context, a *app.App) (string, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Bring-your-own-token mode: run against the caller's own Yandex
	// account instead of the server's default one — if that account is
	// on the allowlist.
	if xToken := r.Header.Get("X-Yandex-Token"); xToken != "" {
		client, _, err := tokenClients.get(xToken, loadAccess())
		if err != nil {
			writeJSON(w, statusForTokenError(err), commandResponse{Error: err.Error()})
			return
		}
		tmpApp := buildApp(client, defaultsCfg, customCfg)
		defer tmpApp.Close()
		out, err := run(ctx, tmpApp)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, commandResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, commandResponse{OK: true, Output: out})
		return
	}

	if defaultApp == nil {
		writeJSON(w, http.StatusUnauthorized, commandResponse{Error: "сервер запущен без своего аккаунта (BYOT по умолчанию) — передайте свой X-Yandex-Token"})
		return
	}
	out, err := run(ctx, defaultApp)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, commandResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, commandResponse{OK: true, Output: out})
}

// statusForTokenError picks the HTTP status for a tokenClientCache.get
// error: 403 if the account was identified fine but isn't allowlisted,
// 401 for anything else (bad/expired token, network failure talking to
// Yandex, etc).
func statusForTokenError(err error) int {
	var na *notAllowedError
	if errors.As(err, &na) {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}

// handleCommandByName is the primary way to run a command over HTTP:
// POST /commands/{name}, one auto-registered URL per dispatcher command
// (GET /commands lists them all with their exact field names/types) —
// built-ins (say, volume, scenario, ...) and everything loaded from
// config.json/commands.json alike, all through the same mechanism (see
// internal/dispatch.Dispatcher.Spec/CallNamed). No "/name ..." line is
// ever built or parsed on this path — the body's JSON fields are handed
// straight through to the command's bound handler by name.
//
//	POST /commands/say    {"text": "привет", "station": "Кухня"}
//	POST /commands/timer  {"minutes": "10", "label": "проверить духовку"}
//	POST /commands/scenarios   {}
//
// station works the same way as /command: a top-level "station" field in
// the body, or the X-Station header — only sent through at all if the
// command's spec says it takes one (see dispatch.CommandSpec.TakesStation).
func handleCommandByName(defaultsCfg, customCfg *app.CustomCommandConfig, defaultApp *app.App, tokenClients *tokenClientCache, loadAccess func() *access.List) http.HandlerFunc {
	// A throwaway App with a nil Client only to read off the dispatcher's
	// registered command specs — safe because Spec/Specs just walk the
	// handler table, they never call a handler (the only thing that
	// would ever touch Client). Specs are identical no matter which
	// account eventually runs the command, so building this once at
	// startup instead of per-request is fine.
	dispatcher := buildApp(nil, defaultsCfg, customCfg).Dispatcher

	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужно имя команды в пути: /commands/<name>"})
			return
		}
		spec, ok := dispatcher.Spec(name)
		if !ok {
			writeJSON(w, http.StatusNotFound, commandResponse{Error: fmt.Sprintf("неизвестная команда: %s (список — GET /commands)", name)})
			return
		}

		body := map[string]json.RawMessage{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: "невалидный JSON: " + err.Error()})
				return
			}
		}

		station, values, err := valuesFromBody(spec, body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: err.Error()})
			return
		}
		if h := r.Header.Get("X-Station"); h != "" && station == "" {
			station = h
		}

		runOnAccount(w, r, defaultApp, tokenClients, defaultsCfg, customCfg, loadAccess,
			func(ctx context.Context, a *app.App) (string, error) {
				out, ok, err := a.ExecuteNamed(ctx, name, station, values)
				if !ok && err == nil {
					err = fmt.Errorf("неизвестная команда: %s", name)
				}
				return out, err
			})
	}
}

// valuesFromBody extracts station (if spec.TakesStation) and every
// declared param from a decoded-but-not-yet-typed JSON object body — the
// shared shape both POST /commands/{name} JSON bodies (see
// handleCommandByName) and MCP tool call arguments (see mcp.go's
// addSpecTool) decode into on their way to ExecuteNamed. A JSON value
// that isn't a plain string (e.g. a bare number) falls back to its
// literal JSON text rather than rejecting the request. Returns an error
// naming the first missing required field.
func valuesFromBody(spec dispatch.CommandSpec, body map[string]json.RawMessage) (station string, values map[string]string, err error) {
	extract := func(name string) (string, bool) {
		raw, present := body[name]
		if !present {
			return "", false
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			s = strings.Trim(string(raw), `"`)
		}
		return s, true
	}

	if spec.TakesStation {
		station, _ = extract("station")
	}

	values = make(map[string]string, len(spec.Params))
	for _, p := range spec.Params {
		v, present := extract(p.Name)
		if !present {
			if !p.Optional {
				return "", nil, fmt.Errorf("нужно поле %q", p.Name)
			}
			values[p.Name] = ""
			continue
		}
		values[p.Name] = v
	}
	return station, values, nil
}

// handleCommandsList answers GET /commands with every command's exact
// POST /commands/{name} body shape: its field names, whether each is
// optional, and whether the command takes a "station" field at all —
// generated straight from the dispatcher, so it can never drift out of
// sync with what handleCommandByName actually accepts.
func handleCommandsList(defaultsCfg, customCfg *app.CustomCommandConfig) http.HandlerFunc {
	specs := buildApp(nil, defaultsCfg, customCfg).Dispatcher.Specs()

	type paramJSON struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Optional bool   `json:"optional,omitempty"`
	}
	type endpointJSON struct {
		Name         string      `json:"name"`
		Category     string      `json:"category,omitempty"`
		Help         string      `json:"help,omitempty"`
		TakesStation bool        `json:"takes_station"`
		Params       []paramJSON `json:"params,omitempty"`
	}
	endpoints := make([]endpointJSON, 0, len(specs))
	for _, spec := range specs {
		params := make([]paramJSON, len(spec.Params))
		for i, p := range spec.Params {
			params[i] = paramJSON{Name: p.Name, Kind: p.Kind, Optional: p.Optional}
		}
		endpoints = append(endpoints, endpointJSON{
			Name: spec.Name, Category: spec.Category, Help: spec.Help,
			TakesStation: spec.TakesStation, Params: params,
		})
	}

	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, endpoints)
	}
}

type stationJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	HouseName string `json:"house_name,omitempty"`
	Platform  string `json:"platform,omitempty"`
}

// handleStations lists the speakers on the account identified by the
// required X-Yandex-Token header (which must be on the access.json
// allowlist) — the discovery step for bring-your-own-token mode: call
// this first to find the station id/name to pass as "station" in
// /command.
func handleStations(tokenClients *tokenClientCache, loadAccess func() *access.List) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		xToken := r.Header.Get("X-Yandex-Token")
		if xToken == "" {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужен заголовок X-Yandex-Token"})
			return
		}
		client, _, err := tokenClients.get(xToken, loadAccess())
		if err != nil {
			writeJSON(w, statusForTokenError(err), commandResponse{Error: err.Error()})
			return
		}
		out := make([]stationJSON, 0, len(client.Speakers))
		for _, d := range client.Speakers {
			s := stationJSON{ID: d.ID, Name: d.Name, HouseName: d.HouseName}
			if d.QuasarInfo != nil {
				s.Platform = d.QuasarInfo.Platform
			}
			out = append(out, s)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleSchedules(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			writeJSON(w, http.StatusOK, []struct{}{})
			return
		}
		tasks := a.Scheduler.List()
		type taskJSON struct {
			ID          int    `json:"id"`
			Spec        string `json:"spec"`
			CommandLine string `json:"command_line"`
		}
		out := make([]taskJSON, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, taskJSON{ID: t.ID, Spec: t.Spec, CommandLine: t.CommandLine})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "ok")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

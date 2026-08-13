// Command yastation-server exposes the same command set as the REPL over
// HTTP, so you can control the station from any program/script/curl call
// on your server, not just an interactive terminal. Every request is
// funnelled through the same single-worker queue as everything else, so
// concurrent requests can't race each other editing the same speaker's
// scenario; each request waits for its own actual result before
// answering (see internal/app.App.Execute).
//
// Two ways to send a command:
//   - POST /command — one endpoint, a "/name ..." line or a
//     {"text": "..."} convenience form (see commandRequest).
//   - POST /commands/{name} — one auto-registered URL per dispatcher
//     command (GET /commands lists them all); config-driven commands
//     (config.json.default, --config/commands.json) take their declared
//     param names directly as JSON fields instead of a line to parse —
//     see handleCommandByName's doc comment for the exact shape.
//
// Two auth modes:
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
//     account, trusted by definition; gated by YASTATION_HTTP_TOKEN like
//     everything else.
//
// The auth modes above are about *whose Yandex account* a request runs
// against. That's orthogonal to YASTATION_HTTP_TOKEN, which is this
// server's own API key (checked via "Authorization: Bearer ...") so
// random callers can't hit your HTTP endpoint at all. It's optional (the
// server logs a warning and runs open without it) — with the allowlist
// in place, an unauthenticated stranger sending a valid X-Yandex-Token
// for an account that isn't on the list gets rejected regardless, so
// YASTATION_HTTP_TOKEN is defense-in-depth rather than the only thing
// standing between your server and abuse.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	addr := envOr("YASTATION_HTTP_ADDR", ":8737")
	token := os.Getenv("YASTATION_HTTP_TOKEN")
	if token == "" {
		log.Println("ВНИМАНИЕ: YASTATION_HTTP_TOKEN не задан — сервер принимает запросы без авторизации.")
		log.Println("(это НЕ токен твоего Яндекс-аккаунта — это отдельный ключ для доступа к самому этому HTTP API)")
	}

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
	// yastation-auth) for requests that don't carry one.
	useDefaultAccount := os.Getenv("YASTATION_USE_DEFAULT_ACCOUNT") != ""

	var a *app.App
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
		a = buildApp(client, defaultsCfg, customCfg)
		defer a.Close()
	}

	tokenClients := newTokenClientCache(20 * time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("POST /command", handleCommand(a, tokenClients, defaultsCfg, customCfg, loadAccess))
	mux.HandleFunc("GET /commands", handleCommandsList(defaultsCfg, customCfg))
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(a, tokenClients, defaultsCfg, customCfg, loadAccess))
	mux.HandleFunc("GET /schedules", handleSchedules(a))
	mux.HandleFunc("GET /stations", handleStations(tokenClients, loadAccess))

	handler := withAuth(token, withLogging(mux))

	log.Println("Слушаю на", addr)
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

func withAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		want := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, `{"ok":false,"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// --- handlers -----------------------------------------------------------

type commandRequest struct {
	// Line is a full command line, e.g. "/say привет" or just free text
	// to be spoken. Takes priority over Text/Station if both are set.
	Line string `json:"line"`

	// Text + Station are a convenience form for the common case: send
	// {"text": "привет", "station": "Кухня"} and it's spoken as-is.
	// Station is optional; omitted means the default speaker. Station
	// can also be given as the X-Station header instead of in the body —
	// useful when Line is used and already has its own station=... arg
	// baked in isn't convenient.
	Text    string `json:"text"`
	Station string `json:"station"`
	// AsCommand sends Text as a voice command (/cmd) instead of TTS
	// (/say) when using the Text/Station convenience form.
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

		line := req.Line
		if line == "" {
			if req.Text == "" {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужно поле line или text"})
				return
			}
			stationArg := ""
			if req.Station != "" {
				stationArg = "station=" + req.Station + " "
			}
			if req.AsCommand {
				line = "/cmd " + stationArg + req.Text
			} else {
				line = "/say " + stationArg + req.Text
			}
		}

		runOnAccount(w, r, defaultApp, tokenClients, defaultsCfg, customCfg, loadAccess,
			func(ctx context.Context, a *app.App) (string, error) { return a.Execute(ctx, line) })
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

// handleCommandByName is the auto-registered per-command endpoint:
// POST /commands/{name} — one URL per dispatcher command (built-ins like
// "say"/"volume" and every command loaded from config.json/commands.json
// alike, see Dispatcher.Names()), instead of building a "/name ..." line
// by hand against the generic /command endpoint.
//
// For commands with a declared param list (anything from config.json or
// a custom commands.json — see internal/app.CustomCommandDef), the body
// takes those exact param names as JSON fields:
//
//	POST /commands/timer  {"minutes": "10", "label": "проверить духовку"}
//
// Built-in Go-coded commands (say, cmd, notify, ...) never had a
// declared param list — they parse their own free-form args — so they
// fall back to a generic positional form instead:
//
//	POST /commands/say  {"args": ["привет"]}
//
// station works the same way as /command: a top-level "station" field in
// the body, or the X-Station header.
func handleCommandByName(defaultApp *app.App, tokenClients *tokenClientCache, defaultsCfg, customCfg *app.CustomCommandConfig, loadAccess func() *access.List) http.HandlerFunc {
	paramNames := paramNamesByCommand(defaultsCfg, customCfg)

	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужно имя команды в пути: /commands/<name>"})
			return
		}

		body := map[string]json.RawMessage{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: "невалидный JSON: " + err.Error()})
				return
			}
		}

		station := ""
		if raw, ok := body["station"]; ok {
			if err := json.Unmarshal(raw, &station); err != nil {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: `поле "station" должно быть строкой`})
				return
			}
		}
		if h := r.Header.Get("X-Station"); h != "" && station == "" {
			station = h
		}

		var argv []string
		if params, ok := paramNames[name]; ok {
			args, missing := argsFromNamedParams(params, body)
			if missing != "" {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: fmt.Sprintf("нужно поле %q", missing)})
				return
			}
			argv = args
		} else if raw, ok := body["args"]; ok {
			if err := json.Unmarshal(raw, &argv); err != nil {
				writeJSON(w, http.StatusBadRequest, commandResponse{Error: `поле "args" должно быть массивом строк`})
				return
			}
		}
		if station != "" {
			argv = append([]string{"station=" + station}, argv...)
		}

		runOnAccount(w, r, defaultApp, tokenClients, defaultsCfg, customCfg, loadAccess,
			func(ctx context.Context, a *app.App) (string, error) { return a.ExecuteArgs(ctx, name, argv) })
	}
}

// paramNamesByCommand indexes config-driven commands (config.json.default
// plus any --config/YASTATION_COMMANDS_FILE commands.json) by name and
// alias, to their declared Params (a "?" suffix marks an optional
// trailing param, same as internal/app.CustomCommandDef.Params) — this is
// what lets /commands/{name} accept named JSON fields instead of a bare
// "args" array for these commands specifically. Built-in Go-coded
// commands never declared a param list and simply aren't in this map.
func paramNamesByCommand(cfgs ...*app.CustomCommandConfig) map[string][]string {
	out := map[string][]string{}
	for _, cfg := range cfgs {
		if cfg == nil {
			continue
		}
		for _, def := range cfg.Commands {
			for _, n := range append([]string{def.Name}, def.Aliases...) {
				out[n] = def.Params
			}
		}
	}
	return out
}

// argsFromNamedParams pulls string values out of body by the declared
// param names, in order — the same optional-trailing-param rule as
// internal/app.bindCustomParams: a "?"-suffixed name may be absent from
// body, and comes out as "". Returns the name of the first missing
// required field as missing, or "" if every required field was present.
func argsFromNamedParams(params []string, body map[string]json.RawMessage) (argv []string, missing string) {
	for _, p := range params {
		name := strings.TrimSuffix(p, "?")
		optional := strings.HasSuffix(p, "?")
		raw, present := body[name]
		if !present {
			if !optional {
				return nil, name
			}
			argv = append(argv, "")
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			// Not a JSON string (e.g. a bare number) — fall back to its
			// literal JSON text rather than rejecting the request.
			s = strings.Trim(string(raw), `"`)
		}
		argv = append(argv, s)
	}
	return argv, ""
}

// handleCommandsList answers GET /commands with every auto-registered
// per-command endpoint name, plus (for config-driven commands) the field
// names its POST /commands/{name} body accepts.
func handleCommandsList(defaultsCfg, customCfg *app.CustomCommandConfig) http.HandlerFunc {
	// A throwaway App with a nil Client only to read off its registered
	// command names — safe because Names() just walks the handler table,
	// it never calls a handler (which is the only thing that would ever
	// touch Client).
	names := buildApp(nil, defaultsCfg, customCfg).Dispatcher.Names()
	sort.Strings(names)
	paramNames := paramNamesByCommand(defaultsCfg, customCfg)

	type endpointJSON struct {
		Name   string   `json:"name"`
		Params []string `json:"params,omitempty"`
	}
	endpoints := make([]endpointJSON, 0, len(names))
	for _, n := range names {
		endpoints = append(endpoints, endpointJSON{Name: n, Params: paramNames[n]})
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

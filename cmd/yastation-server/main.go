// Command yastation-server exposes the same command set as the REPL over
// HTTP, so you can control the station from any program/script/curl call
// on your server, not just an interactive terminal. Every request is
// funnelled through the same single-worker queue as everything else, so
// concurrent requests can't race each other editing the same speaker's
// scenario; each request waits for its own actual result before
// answering (see internal/app.App.Execute).
//
// Two auth modes:
//   - "Bring your own token" (default): a request carrying an
//     X-Yandex-Token header runs against *that* Yandex account. No
//     account of the server's own is needed — nothing to authorize on
//     the box, nothing to leak if the box is compromised. Clients built
//     this way are cached briefly per token (see tokenClientCache) so
//     repeated requests don't redo the login handshake every time.
//   - Own account (opt-in via YASTATION_USE_DEFAULT_ACCOUNT=1): the
//     server also keeps its own pre-authenticated account (from
//     yastation-auth) and uses it for any request that doesn't carry
//     X-Yandex-Token.
//
// The auth modes above are about *whose Yandex account* a request runs
// against. That's orthogonal to YASTATION_HTTP_TOKEN, which is this
// server's own API key (checked via "Authorization: Bearer ...") so
// random callers can't hit your HTTP endpoint at all. It's optional (the
// server logs a warning and runs open without it) but worth setting any
// time the port is reachable from outside localhost — BYOT-only doesn't
// make it optional: without it, anyone can use your server as an
// anonymous relay for *any* Yandex account they hand it a token for,
// burning your bandwidth and CPU on someone else's traffic.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	addr := envOr("YASTATION_HTTP_ADDR", ":8737")
	token := os.Getenv("YASTATION_HTTP_TOKEN")
	if token == "" {
		log.Println("ВНИМАНИЕ: YASTATION_HTTP_TOKEN не задан — сервер принимает запросы без авторизации.")
		log.Println("Задайте переменную окружения, если сервер смотрит наружу, а не только в localhost —")
		log.Println("иначе кто угодно сможет дёргать /command своими X-Yandex-Token через твой сервер.")
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

	// useDefaultAccount: opt-in only. By default the server has no
	// Yandex account of its own — every request must bring its own
	// X-Yandex-Token. Set this to also keep a default account (from
	// yastation-auth) for requests that don't carry one.
	useDefaultAccount := os.Getenv("YASTATION_USE_DEFAULT_ACCOUNT") != ""

	var a *app.App
	if !useDefaultAccount {
		log.Println("BYOT (по умолчанию): своего аккаунта нет, каждый запрос должен нести X-Yandex-Token")
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
	mux.HandleFunc("POST /command", handleCommand(a, tokenClients, defaultsCfg, customCfg))
	mux.HandleFunc("GET /schedules", handleSchedules(a))
	mux.HandleFunc("GET /stations", handleStations(tokenClients))

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
}

type tokenCacheEntry struct {
	client  *quasar.Client
	expires time.Time
}

func newTokenClientCache(ttl time.Duration) *tokenClientCache {
	return &tokenClientCache{ttl: ttl, entries: map[string]*tokenCacheEntry{}}
}

// hashToken never stores or logs the raw token, only a digest, so it
// can't leak through a crash dump, log line, or map key inspection.
func hashToken(xToken string) string {
	sum := sha256.Sum256([]byte(xToken))
	return hex.EncodeToString(sum[:])
}

// get returns a ready quasar.Client for xToken, building and caching one
// (a fresh login handshake + device/scenario refresh) on first use.
func (c *tokenClientCache) get(xToken string) (*quasar.Client, error) {
	key := hashToken(xToken)

	c.mu.Lock()
	c.sweepLocked()
	if e, ok := c.entries[key]; ok {
		e.expires = time.Now().Add(c.ttl)
		client := e.client
		c.mu.Unlock()
		return client, nil
	}
	c.mu.Unlock()

	client, err := quasar.ClientFromXToken(xToken)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[key] = &tokenCacheEntry{client: client, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return client, nil
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

func handleCommand(defaultApp *app.App, tokenClients *tokenClientCache, defaultsCfg, customCfg *app.CustomCommandConfig) http.HandlerFunc {
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

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		// Bring-your-own-token mode: run against the caller's own Yandex
		// account instead of the server's default one.
		if xToken := r.Header.Get("X-Yandex-Token"); xToken != "" {
			client, err := tokenClients.get(xToken)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, commandResponse{Error: "не удалось войти с этим токеном: " + err.Error()})
				return
			}
			tmpApp := buildApp(client, defaultsCfg, customCfg)
			defer tmpApp.Close()
			out, err := tmpApp.Execute(ctx, line)
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

		out, err := defaultApp.Execute(ctx, line)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, commandResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, commandResponse{OK: true, Output: out})
	}
}

type stationJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	HouseName string `json:"house_name,omitempty"`
	Platform  string `json:"platform,omitempty"`
}

// handleStations lists the speakers on the account identified by the
// required X-Yandex-Token header — the discovery step for
// bring-your-own-token mode: call this first to find the station id/name
// to pass as "station" in /command.
func handleStations(tokenClients *tokenClientCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		xToken := r.Header.Get("X-Yandex-Token")
		if xToken == "" {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "нужен заголовок X-Yandex-Token"})
			return
		}
		client, err := tokenClients.get(xToken)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, commandResponse{Error: "не удалось войти с этим токеном: " + err.Error()})
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

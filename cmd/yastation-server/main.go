// Command yastation-server exposes the same command set as the REPL over
// HTTP, so you can control the station from any program/script/curl call
// on your server, not just an interactive terminal. Every request is
// funnelled through the same single-worker queue as everything else, so
// concurrent requests can't race each other editing the same speaker's
// scenario; each request waits for its own actual result before
// answering (see internal/app.App.Execute).
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	addr := envOr("YASTATION_HTTP_ADDR", ":8737")
	token := os.Getenv("YASTATION_HTTP_TOKEN")
	if token == "" {
		log.Println("ВНИМАНИЕ: YASTATION_HTTP_TOKEN не задан — сервер принимает запросы без авторизации.")
		log.Println("Задайте переменную окружения, если сервер смотрит наружу, а не только в localhost.")
	}

	log.Println("Подключаюсь к Яндекс Станции...")
	client, err := quasar.Connect()
	if err != nil {
		log.Fatalf("Не удалось подключиться: %v\nЕсли это первый запуск — авторизуйтесь: go run ./cmd/yastation-auth", err)
	}
	names := make([]string, len(client.Speakers))
	for i, d := range client.Speakers {
		names[i] = d.Name
	}
	log.Printf("Подключено. Колонок найдено: %d (%s)", len(client.Speakers), strings.Join(names, ", "))

	a := app.New(client)
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("POST /command", handleCommand(a))
	mux.HandleFunc("GET /schedules", handleSchedules(a))

	handler := withAuth(token, withLogging(mux))

	log.Println("Слушаю на", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	// Station is optional; omitted means the default speaker.
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

func handleCommand(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req commandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse{Error: "невалидный JSON: " + err.Error()})
			return
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

		out, err := a.Execute(ctx, line)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, commandResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, commandResponse{OK: true, Output: out})
	}
}

func handleSchedules(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

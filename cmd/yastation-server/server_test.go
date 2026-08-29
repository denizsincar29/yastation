package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/dispatch"
	"github.com/denizsincar29/yastation/internal/quasar"
)

// fakeStation is a minimal app.StationAPI for testing the HTTP layer
// without a real Yandex account, mirroring the pattern used in
// cmd/yastation/main_test.go.
type fakeStation struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeStation) record(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return nil
}

func (f *fakeStation) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeStation) Say(station, text string) error {
	return f.record(fmt.Sprintf("say:%s:%s", station, text))
}
func (f *fakeStation) Command(station, text string) error {
	return f.record(fmt.Sprintf("cmd:%s:%s", station, text))
}
func (f *fakeStation) Notify(station, text string, volume float64) error { return f.record("notify") }
func (f *fakeStation) Volume(station string, level float64) error        { return f.record("volume") }
func (f *fakeStation) RunScenario(name string) error                     { return f.record("scenario") }
func (f *fakeStation) ListScenarios() []string                           { return nil }
func (f *fakeStation) Diagnostics() (string, error)                      { return "ok", nil }
func (f *fakeStation) Capabilities(station string) ([]any, error)        { return nil, nil }
func (f *fakeStation) RawCapability(station, capType, instance string, value any) error {
	return nil
}
func (f *fakeStation) SayWhisper(station, text string) error              { return nil }
func (f *fakeStation) PlaySound(station, soundID, soundName string) error { return nil }
func (f *fakeStation) StopEverything(station string) error                { return nil }
func (f *fakeStation) LightScene(station, sceneID string) error           { return nil }
func (f *fakeStation) Weather(station string) error                       { return nil }
func (f *fakeStation) PlayMusic(station string) error                     { return nil }
func (f *fakeStation) Refresh() error                                     { return nil }

// --- token cache -----------------------------------------------------

func TestHashTokenDeterministicAndDistinct(t *testing.T) {
	a := hashToken("token-a")
	b := hashToken("token-a")
	c := hashToken("token-b")
	if a != b {
		t.Fatal("hashToken should be deterministic")
	}
	if a == c {
		t.Fatal("different tokens should hash differently")
	}
	if strings.Contains(a, "token-a") {
		t.Fatal("hash should not contain the raw token")
	}
}

func TestSweepLockedDropsExpiredOnly(t *testing.T) {
	c := newTokenClientCache(time.Minute)
	c.entries["expired"] = &tokenCacheEntry{expires: time.Now().Add(-time.Second)}
	c.entries["fresh"] = &tokenCacheEntry{expires: time.Now().Add(time.Minute)}

	c.mu.Lock()
	c.sweepLocked()
	c.mu.Unlock()

	if _, ok := c.entries["expired"]; ok {
		t.Fatal("expired entry should have been swept")
	}
	if _, ok := c.entries["fresh"]; !ok {
		t.Fatal("fresh entry should still be present")
	}
}

func TestTokenClientCacheGetDeniesUnlistedAccount(t *testing.T) {
	c := newTokenClientCache(time.Minute)
	buildCalls := 0
	c.resolveIdentity = func(xToken string) (quasar.Identity, error) {
		return quasar.Identity{UID: "1", RealName: "Кто-то"}, nil
	}
	c.buildClient = func(xToken string) (*quasar.Client, error) {
		buildCalls++
		return &quasar.Client{}, nil
	}

	list := &access.List{} // empty — nobody allowed
	_, _, err := c.get("tok", list)
	if err == nil {
		t.Fatal("expected error for unlisted account")
	}
	var na *notAllowedError
	if !errors.As(err, &na) {
		t.Fatalf("expected *notAllowedError, got %T: %v", err, err)
	}
	if buildCalls != 0 {
		t.Fatal("buildClient must not run for a denied account")
	}
}

func TestTokenClientCacheGetAllowsAndCachesListedAccount(t *testing.T) {
	c := newTokenClientCache(time.Minute)
	resolveCalls, buildCalls := 0, 0
	fakeClient := &quasar.Client{}
	c.resolveIdentity = func(xToken string) (quasar.Identity, error) {
		resolveCalls++
		return quasar.Identity{UID: "1", RealName: "Дениз"}, nil
	}
	c.buildClient = func(xToken string) (*quasar.Client, error) {
		buildCalls++
		return fakeClient, nil
	}

	list := &access.List{}
	list.Add(access.Entry{Name: "Дениз", UID: "1"})

	client, id, err := c.get("tok", list)
	if err != nil {
		t.Fatal(err)
	}
	if client != fakeClient || id.UID != "1" {
		t.Fatalf("client=%v id=%v", client, id)
	}

	// Second call with the same token should hit the cache: no repeated
	// network resolution/build.
	client2, _, err := c.get("tok", list)
	if err != nil {
		t.Fatal(err)
	}
	if client2 != fakeClient {
		t.Fatal("expected cached client on second call")
	}
	if resolveCalls != 1 || buildCalls != 1 {
		t.Fatalf("expected exactly one resolve/build call, got resolve=%d build=%d", resolveCalls, buildCalls)
	}
}

func TestTokenClientCacheGetRevokesEvenWithWarmCache(t *testing.T) {
	c := newTokenClientCache(time.Minute)
	c.resolveIdentity = func(xToken string) (quasar.Identity, error) {
		return quasar.Identity{UID: "1", RealName: "Дениз"}, nil
	}
	c.buildClient = func(xToken string) (*quasar.Client, error) {
		return &quasar.Client{}, nil
	}

	allowed := &access.List{}
	allowed.Add(access.Entry{Name: "Дениз", UID: "1"})
	if _, _, err := c.get("tok", allowed); err != nil {
		t.Fatal(err)
	}

	// Simulate `yastation-access remove` happening between requests: the
	// client is still warm in cache, but the fresh list no longer has
	// this uid.
	revoked := &access.List{}
	_, _, err := c.get("tok", revoked)
	if err == nil {
		t.Fatal("expected access to be denied immediately after revocation, even with a cached client")
	}
	var na *notAllowedError
	if !errors.As(err, &na) {
		t.Fatalf("expected *notAllowedError, got %T: %v", err, err)
	}
}

// --- handlers ------------------------------------------------------------

func emptyAccessList() *access.List { return &access.List{} }

func TestHandleHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleCommandInvalidJSON(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCommandMissingLineAndText(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCommandDefaultAppSuccess(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(`{"text":"привет","station":"Кухня"}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var resp commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("resp=%+v", resp)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "say:Кухня:привет" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleCommandXStationHeaderFillsStation(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(`{"text":"привет"}`))
	req.Header.Set("X-Station", "Кухня")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "say:Кухня:привет" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleCommandAsCommandFlag(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(`{"text":"включи радио","as_command":true}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if calls := f.Calls(); len(calls) != 1 || calls[0] != "cmd::включи радио" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleStationsRequiresXYandexToken(t *testing.T) {
	h := handleStations(newTokenClientCache(time.Minute), emptyAccessList)
	req := httptest.NewRequest(http.MethodGet, "/stations", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCommandBYOTPropagatesBuildError checks that when a caller
// supplies X-Yandex-Token, a failure to build/login with that token
// surfaces as a 401 rather than falling back to the server's own
// default account (which would silently run the command against the
// wrong account instead of failing loudly).
func TestHandleCommandBYOTFailsClosedOnBadToken(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	h := handleCommand(a, newTokenClientCache(time.Minute), nil, nil, emptyAccessList)
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(`{"text":"привет"}`))
	req.Header.Set("X-Yandex-Token", "definitely-not-a-real-token")
	rec := httptest.NewRecorder()

	// This will attempt a real network call to Yandex and fail (invalid
	// token) rather than succeed -- we only assert it does NOT fall back
	// to the default app's fake station, i.e. the default fake never
	// gets a call recorded for this request.
	h(rec, req)

	if len(f.Calls()) != 0 {
		t.Fatalf("BYOT request must not run against the default account's client: calls=%v", f.Calls())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("expected a failure status for a bogus token, got 200: %s", rec.Body.String())
	}
}

// --- per-command endpoints -------------------------------------------

func testCustomCfg() *app.CustomCommandConfig {
	return &app.CustomCommandConfig{Commands: []app.CustomCommandDef{
		{
			Name:     "timer",
			Params:   []string{"minutes", "label?"},
			Template: "поставь таймер на $minutes минут $label",
			Kind:     "command",
		},
	}}
}

func TestHandleCommandByNameConfigCommandUsesNamedParams(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	if err := a.RegisterCustomCommands(testCustomCfg()); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(testCustomCfg(), nil, a, newTokenClientCache(time.Minute), emptyAccessList))

	req := httptest.NewRequest(http.MethodPost, "/commands/timer", strings.NewReader(`{"minutes":"10","label":"проверить духовку","station":"Кухня"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	calls := f.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "Кухня") || !strings.Contains(calls[0], "10 минут проверить духовку") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleCommandByNameConfigCommandOptionalParamOmitted(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	if err := a.RegisterCustomCommands(testCustomCfg()); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(testCustomCfg(), nil, a, newTokenClientCache(time.Minute), emptyAccessList))

	req := httptest.NewRequest(http.MethodPost, "/commands/timer", strings.NewReader(`{"minutes":"5"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCommandByNameConfigCommandMissingRequiredParam(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	if err := a.RegisterCustomCommands(testCustomCfg()); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(testCustomCfg(), nil, a, newTokenClientCache(time.Minute), emptyAccessList))

	req := httptest.NewRequest(http.MethodPost, "/commands/timer", strings.NewReader(`{"label":"духовку"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCommandByNameBuiltinUsesNamedParams checks that a built-in
// Go-coded command (not from any commands.json) goes through the exact
// same named-JSON-field mechanism as a config-driven one — no more
// generic {"args": [...]} positional fallback.
func TestHandleCommandByNameBuiltinUsesNamedParams(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(nil, nil, a, newTokenClientCache(time.Minute), emptyAccessList))

	req := httptest.NewRequest(http.MethodPost, "/commands/say", strings.NewReader(`{"text":"привет","station":"Кухня"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "say:Кухня:привет" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestHandleCommandByNameUnknownCommand(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /commands/{name}", handleCommandByName(nil, nil, a, newTokenClientCache(time.Minute), emptyAccessList))

	req := httptest.NewRequest(http.MethodPost, "/commands/nope", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCommandsListIncludesBuiltinsAndConfigParams(t *testing.T) {
	h := handleCommandsList(testCustomCfg(), nil)
	req := httptest.NewRequest(http.MethodGet, "/commands", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var endpoints []struct {
		Name         string `json:"name"`
		TakesStation bool   `json:"takes_station"`
		Params       []struct {
			Name     string `json:"name"`
			Optional bool   `json:"optional,omitempty"`
		} `json:"params,omitempty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &endpoints); err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, e := range endpoints {
		byName[e.Name] = i
	}
	sayIdx, ok := byName["say"]
	if !ok || !endpoints[sayIdx].TakesStation || len(endpoints[sayIdx].Params) != 1 || endpoints[sayIdx].Params[0].Name != "text" {
		t.Fatalf("expected builtin 'say' with a required 'text' param: %+v (ok=%v)", endpoints, ok)
	}
	timerIdx, ok := byName["timer"]
	if !ok {
		t.Fatalf("expected config command 'timer' in list: %v", byName)
	}
	timerParams := endpoints[timerIdx].Params
	if len(timerParams) != 2 || timerParams[0].Name != "minutes" || timerParams[0].Optional ||
		timerParams[1].Name != "label" || !timerParams[1].Optional {
		t.Fatalf("expected timer params [minutes(required) label(optional)], got %+v", timerParams)
	}
}

func TestValuesFromBodyMissingRequired(t *testing.T) {
	spec := dispatch.CommandSpec{Params: []dispatch.Param{{Name: "when"}, {Name: "text"}}}
	_, _, err := valuesFromBody(spec, map[string]json.RawMessage{
		"when": json.RawMessage(`"завтра"`),
	})
	if err == nil {
		t.Fatal("expected an error for the missing required 'text' field")
	}
}

func TestValuesFromBodyOptionalDefaultsEmpty(t *testing.T) {
	spec := dispatch.CommandSpec{Params: []dispatch.Param{{Name: "minutes"}, {Name: "label", Optional: true}}}
	_, values, err := valuesFromBody(spec, map[string]json.RawMessage{
		"minutes": json.RawMessage(`"10"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if values["minutes"] != "10" || values["label"] != "" {
		t.Fatalf("values=%v", values)
	}
}

func TestValuesFromBodyStation(t *testing.T) {
	spec := dispatch.CommandSpec{TakesStation: true}
	station, _, err := valuesFromBody(spec, map[string]json.RawMessage{
		"station": json.RawMessage(`"Кухня"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if station != "Кухня" {
		t.Fatalf("station=%q", station)
	}
}

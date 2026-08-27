package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpFakeClient is a *quasar.Client with a couple of speakers, good
// enough for exercising the MCP layer without touching the real Yandex
// API. Most alice_* tools (alice_say, alice_cmd, ...) still end up
// calling quasar.Client's real HTTP methods (Say/Command/...), which
// would fail against a fake Session — these tests stick to tool
// discovery (alice_help/alice_stations) and the auth gate, which don't
// need a live station.
func mcpFakeClient() *quasar.Client {
	return &quasar.Client{
		Speakers: []quasar.Device{
			{ID: "dev1", Name: "Кухня"},
			{ID: "dev2", Name: "Спальня", HouseName: "Дом"},
		},
	}
}

func newMCPTestMux(t *testing.T, allowedUID string, defaultClient *quasar.Client) http.Handler {
	t.Helper()

	tokenClients := newTokenClientCache(time.Minute)
	tokenClients.resolveIdentity = func(xToken string) (quasar.Identity, error) {
		return quasar.Identity{UID: "uid-" + xToken, RealName: "Тестовый пользователь"}, nil
	}
	tokenClients.buildClient = func(xToken string) (*quasar.Client, error) {
		return mcpFakeClient(), nil
	}

	loadAccess := func() *access.List {
		return &access.List{Entries: []access.Entry{{UID: allowedUID}}}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("/mcp", mcpRoute(defaultClient, tokenClients, nil, nil, loadAccess))
	return mux
}

func TestMCPRejectsMissingTokenWithoutDefaultAccount(t *testing.T) {
	srv := httptest.NewServer(newMCPTestMux(t, "uid-good-token", nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without X-Yandex-Token and no default account (never 401 — see mcpAccountMiddleware), got %d", resp.StatusCode)
	}
	if h := resp.Header.Get("WWW-Authenticate"); h != "" {
		t.Fatalf("must never set WWW-Authenticate (it's the RFC9728 cue for MCP clients to start OAuth discovery), got %q", h)
	}
}

func TestMCPFallsBackToDefaultAccountWhenNoToken(t *testing.T) {
	srv := httptest.NewServer(newMCPTestMux(t, "uid-good-token", mcpFakeClient()))
	defer srv.Close()

	// No X-Yandex-Token at all — should still work, running against the
	// server's own default account, same as /command's runOnAccount.
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect without token (default account) should succeed: %v", err)
	}
	defer session.Close()

	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
}

func TestMCPRejectsUnallowedToken(t *testing.T) {
	srv := httptest.NewServer(newMCPTestMux(t, "uid-good-token", nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("X-Yandex-Token", "not-allowed-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a token whose uid isn't allowlisted, got %d", resp.StatusCode)
	}
}

// TestMCPEndToEnd drives the server through a real MCP client (same SDK
// the server uses) over the streamable-HTTP transport: connect with the
// required headers, list tools, and call alice_help/alice_stations.
func TestMCPEndToEnd(t *testing.T) {
	const goodToken = "good-token"
	srv := httptest.NewServer(newMCPTestMux(t, "uid-"+goodToken, nil))
	defer srv.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)

	transport := &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: &mcpHeaderInjectingRoundTripper{
				base: http.DefaultTransport,
				headers: map[string]string{
					"X-Yandex-Token": goodToken,
					"X-Station":      "Кухня",
				},
			},
		},
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"alice_say": false, "alice_cmd": false, "alice_notify": false,
		"alice_volume": false, "alice_scenario": false,
		"alice_help": false, "alice_stations": false,
	}
	for _, tool := range toolsResult.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected tool %q in tools/list", name)
		}
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alice_stations"})
	if err != nil {
		t.Fatalf("CallTool alice_stations: %v", err)
	}
	if res.IsError {
		t.Fatalf("alice_stations returned an error result: %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	if !strings.Contains(text.Text, "Кухня") || !strings.Contains(text.Text, "Спальня") {
		t.Fatalf("expected both speakers listed, got: %s", text.Text)
	}

	helpRes, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alice_help"})
	if err != nil {
		t.Fatalf("CallTool alice_help: %v", err)
	}
	if helpRes.IsError {
		t.Fatalf("alice_help returned an error result: %+v", helpRes.Content)
	}
}

// mcpHeaderInjectingRoundTripper attaches fixed headers to every
// outgoing request, simulating how a remote-MCP-connector config
// (static per-connector headers) would authenticate against this
// server.
type mcpHeaderInjectingRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *mcpHeaderInjectingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

package glagol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// fakeStation is a minimal in-process stand-in for a real speaker: it
// accepts a Glagol-shaped WebSocket connection, checks the token, and
// replies to sendText commands. Lets us test Client.SendText end-to-end
// without needing an actual Yandex device.
func fakeStation(t *testing.T, wantToken string, reply func(text string) response) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		ctx := context.Background()
		for {
			var msg envelope
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return // client closed, test is over
			}
			if msg.ConversationToken != wantToken {
				_ = wsjson.Write(ctx, conn, response{ID: msg.ID, Error: "bad token"})
				continue
			}
			resp := reply(msg.Payload.Text)
			resp.ID = msg.ID
			_ = wsjson.Write(ctx, conn, resp)
		}
	}))
	return srv
}

// dialFake connects Client to a httptest.NewTLSServer, bypassing cert
// verification the same way Dial does for a real LAN device.
func dialFake(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	hostPort := strings.TrimPrefix(srv.URL, "https://")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, err := Dial(ctx, hostPort, token)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

func TestSendTextRoundTrip(t *testing.T) {
	const token = "test-token-123"
	var gotText string
	srv := fakeStation(t, token, func(text string) response {
		gotText = text
		return response{}
	})
	defer srv.Close()

	c := dialFake(t, srv, token)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.SendText(ctx, "привет со станции"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if gotText != "привет со станции" {
		t.Fatalf("got %q", gotText)
	}
}

func TestSendTextSurfacesDeviceError(t *testing.T) {
	const token = "tok"
	srv := fakeStation(t, token, func(text string) response {
		return response{Error: "device busy"}
	})
	defer srv.Close()

	c := dialFake(t, srv, token)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.SendText(ctx, "привет")
	if err == nil || !strings.Contains(err.Error(), "device busy") {
		t.Fatalf("expected device error, got %v", err)
	}
}

func TestSendTextWrongTokenErrors(t *testing.T) {
	srv := fakeStation(t, "correct-token", func(text string) response {
		return response{}
	})
	defer srv.Close()

	c := dialFake(t, srv, "wrong-token")
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.SendText(ctx, "привет")
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("expected bad token error, got %v", err)
	}
}

func TestSendTextTimesOutIfNoReply(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		// accept the connection but never reply
		var msg envelope
		_ = wsjson.Read(context.Background(), conn, &msg)
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := dialFake(t, srv, "tok")
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := c.SendText(ctx, "привет")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestNewIDIsUniqueAndHex(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newID()
		if len(id) != 32 {
			t.Fatalf("expected 32 hex chars, got %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = true
	}
}

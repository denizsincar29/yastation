package glagol

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Client is one open Glagol connection to a single speaker on the local
// network. Not safe for concurrent SendText calls from multiple
// goroutines at once — internal/app's queue is what serializes access in
// this project, same as it does for the cloud path.
type Client struct {
	conn  *websocket.Conn
	token string
	mu    sync.Mutex
}

// envelope is the message shape the device expects on the wire.
type envelope struct {
	ConversationToken string  `json:"conversationToken"`
	ID                string  `json:"id"`
	SentTime          int64   `json:"sentTime"`
	Payload           payload `json:"payload"`
}

type payload struct {
	Command string `json:"command"`
	Text    string `json:"text,omitempty"`
}

// response is the shape of what the device sends back. Extra fields are
// ignored — we only care about correlating by ID and surfacing errors.
type response struct {
	ID    string          `json:"id"`
	State json.RawMessage `json:"state,omitempty"`
	Error string          `json:"errorText,omitempty"`
}

// Dial opens a Glagol WebSocket connection to a speaker at host:port
// (e.g. "192.168.1.42:1961"). The device serves a self-signed TLS
// certificate — connections use wss:// with certificate verification
// disabled, same as every other Glagol client implementation does,
// because there's no public CA involved for a LAN-only device.
func Dial(ctx context.Context, hostPort, token string) (*Client, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // LAN device, self-signed by design
		},
	}
	conn, _, err := websocket.Dial(ctx, "wss://"+hostPort+"/", &websocket.DialOptions{
		HTTPClient: httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("glagol: не смог подключиться к %s: %w", hostPort, err)
	}
	conn.SetReadLimit(1 << 20)
	return &Client{conn: conn, token: token}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// SendText sends text to the speaker as if it had been said out loud
// (command == "sendText", the one Glagol command type that's confirmed
// well-documented public behaviour — see PROTOCOL_NOTES.md for the
// caveat on whether a separate raw-TTS command exists locally). Waits for
// the device's matching response (by id) or ctx's deadline, whichever
// comes first.
func (c *Client) SendText(ctx context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := newID()
	msg := envelope{
		ConversationToken: c.token,
		ID:                id,
		SentTime:          time.Now().UnixMilli(),
		Payload:           payload{Command: "sendText", Text: text},
	}
	if err := wsjson.Write(ctx, c.conn, msg); err != nil {
		return fmt.Errorf("glagol: отправка не удалась: %w", err)
	}

	for {
		var resp response
		if err := wsjson.Read(ctx, c.conn, &resp); err != nil {
			return fmt.Errorf("glagol: не дождался ответа станции: %w", err)
		}
		if resp.ID != id {
			continue // some other event, e.g. a state push; keep waiting for our reply
		}
		if resp.Error != "" {
			return fmt.Errorf("glagol: станция вернула ошибку: %s", resp.Error)
		}
		return nil
	}
}

func newID() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 32)
	for i := range b {
		b[i] = hex[rand.Intn(16)]
	}
	return string(b)
}

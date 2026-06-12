package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/types"
)

// capture holds the last request seen by the test server.
type capture struct {
	mu      sync.Mutex
	method  string
	path    string
	query   string
	headers http.Header
	body    []byte
}

func TestReplayForwardsRequest(t *testing.T) {
	var cap capture

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.headers = r.Header.Clone()
		cap.body = body
		cap.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	event := types.WebhookEvent{
		ID:     "test01",
		Method: http.MethodPost,
		Path:   "/api/events",
		Query:  "foo=bar",
		Headers: map[string][]string{
			"Content-Type":  {"application/json"},
			"X-Custom-Hook": {"sig-abc"},
		},
		Body: []byte(`{"action":"opened"}`),
	}

	replay(event, ts.URL)

	cap.mu.Lock()
	defer cap.mu.Unlock()

	if cap.method != http.MethodPost {
		t.Errorf("method: got %q, want POST", cap.method)
	}
	if cap.path != "/api/events" {
		t.Errorf("path: got %q, want /api/events", cap.path)
	}
	if cap.query != "foo=bar" {
		t.Errorf("query: got %q, want foo=bar", cap.query)
	}
	if cap.headers.Get("X-Custom-Hook") != "sig-abc" {
		t.Errorf("header X-Custom-Hook: got %q", cap.headers.Get("X-Custom-Hook"))
	}
	if cap.headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type: got %q", cap.headers.Get("Content-Type"))
	}
	if string(cap.body) != `{"action":"opened"}` {
		t.Errorf("body: got %q", cap.body)
	}
}

func TestReplayNoQueryString(t *testing.T) {
	var cap capture

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.query = r.URL.RawQuery
		cap.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	replay(types.WebhookEvent{
		ID:     "test02",
		Method: http.MethodPost,
		Path:   "/ping",
		Query:  "",
		Body:   nil,
	}, ts.URL)

	cap.mu.Lock()
	defer cap.mu.Unlock()

	if cap.query != "" {
		t.Errorf("expected empty query, got %q", cap.query)
	}
}

func TestReplayInvalidMethodIsHandled(t *testing.T) {
	// A method containing a newline is rejected by http.NewRequest —
	// replay must log and return without panicking.
	replay(types.WebhookEvent{
		ID:     "err01",
		Method: "INVALID\nMETHOD",
		Path:   "/foo",
	}, "http://localhost:9") // port 9 is discard; never reached
}

func TestReplayTargetDownIsHandled(t *testing.T) {
	// Nothing is listening on port 9 — replay must log and return.
	replay(types.WebhookEvent{
		ID:     "err02",
		Method: http.MethodPost,
		Path:   "/foo",
	}, "http://localhost:9")
}

// wsUpgrader is reused across WS test helpers.
var wsUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// newWSServer starts an httptest server that upgrades to WebSocket, calls fn,
// then closes. Returns the ws:// URL.
func newWSServer(t *testing.T, fn func(*websocket.Conn)) (string, func()) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		fn(conn)
		conn.Close()
	}))
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	return url, ts.Close
}

func TestConnectDialError(t *testing.T) {
	err := connect("ws://localhost:9", "http://localhost:9")
	if err == nil {
		t.Fatal("expected error dialing closed port")
	}
}

func TestConnectReceivesAndReplays(t *testing.T) {
	// replayTarget captures incoming replayed requests.
	received := make(chan struct{}, 1)
	replayTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer replayTarget.Close()

	event := types.WebhookEvent{
		ID:     "ev01",
		Method: http.MethodPost,
		Path:   "/hook/test",
		Headers: map[string][]string{
			"X-Test": {"1"},
		},
		Body: []byte(`{}`),
	}
	data, _ := json.Marshal(event)

	wsURL, close := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, data)
		// Brief pause so the client reads the message before we close.
		time.Sleep(30 * time.Millisecond)
	})
	defer close()

	// connect returns when the WS server closes.
	go connect(wsURL, replayTarget.URL)

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("replay target did not receive the forwarded request")
	}
}

func TestConnectInvalidJSONContinues(t *testing.T) {
	// Server sends invalid JSON then closes; connect should log, continue,
	// then return a read error — not panic.
	done := make(chan error, 1)

	wsURL, closeTS := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, []byte("not-json"))
		time.Sleep(20 * time.Millisecond)
	})
	defer closeTS()

	go func() { done <- connect(wsURL, "http://localhost:9") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected read error after server close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connect did not return after server closed")
	}
}

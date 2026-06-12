package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/types"
)

func TestHubGetOrCreate(t *testing.T) {
	h := newHub()
	ch1 := h.getOrCreate("foo")
	ch2 := h.getOrCreate("foo")
	if ch1 != ch2 {
		t.Fatal("same name must return same channel")
	}
	if h.getOrCreate("bar") == ch1 {
		t.Fatal("different names must return different channels")
	}
}

func TestChannelAddRemoveCount(t *testing.T) {
	ch := &channel{subscribers: make(map[*subscriber]struct{})}
	s := &subscriber{send: make(chan []byte, 4)}

	ch.add(s)
	if got := ch.count(); got != 1 {
		t.Fatalf("count after add: got %d, want 1", got)
	}
	ch.remove(s)
	if got := ch.count(); got != 0 {
		t.Fatalf("count after remove: got %d, want 0", got)
	}
}

func TestChannelBroadcast(t *testing.T) {
	ch := &channel{subscribers: make(map[*subscriber]struct{})}
	s1 := &subscriber{send: make(chan []byte, 4)}
	s2 := &subscriber{send: make(chan []byte, 4)}
	ch.add(s1)
	ch.add(s2)

	n := ch.broadcast([]byte("hello"))
	if n != 2 {
		t.Fatalf("broadcast count: got %d, want 2", n)
	}
	for i, s := range []*subscriber{s1, s2} {
		select {
		case got := <-s.send:
			if string(got) != "hello" {
				t.Errorf("s%d: got %q, want \"hello\"", i+1, got)
			}
		default:
			t.Errorf("s%d did not receive message", i+1)
		}
	}
}

func TestChannelBroadcastDropsSlowSubscriber(t *testing.T) {
	ch := &channel{subscribers: make(map[*subscriber]struct{})}
	// unbuffered — always full, should be dropped without blocking
	ch.add(&subscriber{send: make(chan []byte, 0)})

	done := make(chan struct{})
	go func() {
		ch.broadcast([]byte("drop"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on slow subscriber")
	}
}

func TestHookHandlerReturns200(t *testing.T) {
	srv := newServer()
	ts := httptest.NewServer(buildRouter(srv))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/ci", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
}

func TestSubscribeHandlerUpgradeError(t *testing.T) {
	srv := newServer()
	ts := httptest.NewServer(buildRouter(srv))
	defer ts.Close()

	// Plain HTTP GET (no WS upgrade headers) — upgrader writes 400 and returns.
	resp, err := http.Get(ts.URL + "/subscribe/testchan")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 from failed upgrade, got %d", resp.StatusCode)
	}
}

func TestWebSocketReceivesWebhookEvent(t *testing.T) {
	srv := newServer()
	ts := httptest.NewServer(buildRouter(srv))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/subscribe/ci"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	time.Sleep(20 * time.Millisecond) // let subscription register

	body := `{"ref":"refs/heads/main"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/ci", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatalf("webhook post: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	var event types.WebhookEvent
	if err := json.Unmarshal(msg, &event); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if event.Method != http.MethodPost {
		t.Errorf("method: got %q, want POST", event.Method)
	}
	if string(event.Body) != body {
		t.Errorf("body: got %q, want %q", event.Body, body)
	}
	if event.Headers["X-Github-Event"][0] != "push" {
		t.Errorf("header: got %v", event.Headers["X-Github-Event"])
	}
	if event.ID == "" {
		t.Error("ID must not be empty")
	}
}

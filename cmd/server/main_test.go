package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/store"
	"github.com/whim-proxy/internal/types"
	"github.com/whim-proxy/internal/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// failWriter is an http.ResponseWriter whose Write always returns an error.
type failWriter struct{ http.ResponseWriter }

func (f *failWriter) Write([]byte) (int, error) { return 0, errors.New("write error") }

// panicOnFatalLogger returns a logger that panics instead of calling os.Exit
// on Fatal, so tests can catch the panic and verify the code path executed.
func panicOnFatalLogger() *zap.Logger {
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()),
		zapcore.AddSync(io.Discard),
		zapcore.FatalLevel,
	)
	return zap.New(core, zap.WithFatalHook(zapcore.WriteThenPanic))
}

// testStore is a configurable EventStore stub for error-path tests.
type testStore struct {
	pushErr    error
	recentErr  error
	recentData []types.WebhookEvent
}

func (s *testStore) Push(_ string, _ types.WebhookEvent) error {
	return s.pushErr
}

func (s *testStore) Recent(_ string, _ int) ([]types.WebhookEvent, error) {
	return s.recentData, s.recentErr
}

func newTestServer() (*server, *httptest.Server) {
	srv := newServer(zap.NewNop(), store.NewMemory(100))
	ts := httptest.NewServer(buildRouter(zap.NewNop(), srv))
	return srv, ts
}

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
	_, ts := newTestServer()
	defer ts.Close()

	ch := uuid.New()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+ch, strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Whim-Proxy-Server"); got != version {
		t.Errorf("X-Whim-Proxy-Server: got %q, want %q", got, version)
	}
}

func TestHookHandlerRejects400OnInvalidChannel(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/not-a-uuid", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestSubscribeHandlerRejects400OnInvalidChannel(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/subscribe/not-a-uuid")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestSubscribeHandlerUpgradeError(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	// Plain HTTP GET (no WS upgrade headers) — upgrader writes 400 and returns.
	resp, err := http.Get(ts.URL + "/subscribe/" + uuid.New())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 from failed upgrade, got %d", resp.StatusCode)
	}
}

func TestWebSocketReceivesWebhookEvent(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	ch := uuid.New()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/subscribe/" + ch
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	time.Sleep(20 * time.Millisecond) // let subscription register

	body := `{"ref":"refs/heads/main"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+ch, strings.NewReader(body))
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

func TestLogsHandlerReturnsEvents(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	ch := uuid.New()

	// Post two webhooks.
	for _, body := range []string{`{"n":1}`, `{"n":2}`} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+ch, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if _, err := http.DefaultClient.Do(req); err != nil {
			t.Fatalf("post: %v", err)
		}
	}

	resp, err := http.Get(ts.URL + "/logs/" + ch)
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var events []types.WebhookEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if string(events[0].Body) != `{"n":1}` {
		t.Errorf("first event body: got %q", events[0].Body)
	}
}

func TestLogsHandlerEmptyChannel(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/logs/" + uuid.New())
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	raw, _ := io.ReadAll(resp.Body)
	var events []types.WebhookEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("want empty array, got %d events", len(events))
	}
}

func TestLogsHandlerRejects400OnInvalidChannel(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/logs/not-a-uuid")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestLogsHandlerCapsAtTen(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	ch := uuid.New()
	for i := 0; i < 15; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+ch, strings.NewReader(`{}`))
		http.DefaultClient.Do(req)
	}

	resp, _ := http.Get(ts.URL + "/logs/" + ch)
	defer resp.Body.Close()

	var events []types.WebhookEvent
	json.NewDecoder(resp.Body).Decode(&events)
	if len(events) != 10 {
		t.Errorf("want 10, got %d", len(events))
	}
}

// --- buildLogger ---

func TestBuildLoggerValidLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if _, err := buildLogger(level, false); err != nil {
			t.Errorf("level %q (console): %v", level, err)
		}
		if _, err := buildLogger(level, true); err != nil {
			t.Errorf("level %q (json): %v", level, err)
		}
	}
}

func TestBuildLoggerInvalidLevel(t *testing.T) {
	if _, err := buildLogger("invalid", false); err == nil {
		t.Error("expected error for invalid level")
	}
}

// --- statusResponseWriter.Hijack ---

func TestHijackNotSupported(t *testing.T) {
	w := &statusResponseWriter{ResponseWriter: httptest.NewRecorder()}
	_, _, err := w.Hijack()
	if err == nil {
		t.Error("expected error when ResponseWriter does not implement Hijacker")
	}
}

// --- hookHandler error paths (direct handler calls) ---

func TestHookHandlerBodyReadError(t *testing.T) {
	srv := newServer(zap.NewNop(), store.NewMemory(10))
	ch := uuid.New()

	req := httptest.NewRequest(http.MethodPost, "/hook/"+ch, iotest.ErrReader(errors.New("read fail")))
	req = mux.SetURLVars(req, map[string]string{"channel": ch})
	w := httptest.NewRecorder()
	srv.hookHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}
}

func TestHookHandlerStorePushError(t *testing.T) {
	srv := newServer(zap.NewNop(), &testStore{pushErr: errors.New("store down")})
	ch := uuid.New()

	req := httptest.NewRequest(http.MethodPost, "/hook/"+ch, strings.NewReader(`{}`))
	req = mux.SetURLVars(req, map[string]string{"channel": ch})
	w := httptest.NewRecorder()
	srv.hookHandler(w, req)

	// Push error is logged but the response is still 200.
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}

// --- logsHandler error paths (direct handler calls) ---

func TestLogsHandlerStoreError(t *testing.T) {
	srv := newServer(zap.NewNop(), &testStore{recentErr: errors.New("store down")})
	ch := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/logs/"+ch, nil)
	req = mux.SetURLVars(req, map[string]string{"channel": ch})
	w := httptest.NewRecorder()
	srv.logsHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}
}

func TestLogsHandlerNilEvents(t *testing.T) {
	// Store returns nil slice without error; handler must encode [] not null.
	srv := newServer(zap.NewNop(), &testStore{recentData: nil, recentErr: nil})
	ch := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/logs/"+ch, nil)
	req = mux.SetURLVars(req, map[string]string{"channel": ch})
	w := httptest.NewRecorder()
	srv.logsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body == "null" {
		t.Error("response must be [] not null")
	}
}

// --- hookHandler: json.Indent else branch (non-JSON body with subscriber) ---

func TestHookHandlerPayloadInvalidJSON(t *testing.T) {
	_, ts := newTestServer()
	defer ts.Close()

	ch := uuid.New()

	// Connect a subscriber so n >= 1 during broadcast.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/subscribe/" + ch
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	time.Sleep(20 * time.Millisecond)

	// Post a non-JSON body; triggers the json.Indent else branch in hookHandler.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+ch, strings.NewReader("not-json"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
}

// --- initStore ---

func TestInitStoreMemory(t *testing.T) {
	s := initStore(zap.NewNop(), "", 0, 100)
	if s == nil {
		t.Fatal("expected non-nil store")
	}
	s.Push("a", types.WebhookEvent{ID: "1"})
	got, _ := s.Recent("a", 10)
	if len(got) != 1 {
		t.Errorf("want 1, got %d", len(got))
	}
}

func TestInitStoreRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	s := initStore(zap.NewNop(), "redis://"+mr.Addr(), 0, 100)
	if s == nil {
		t.Fatal("expected non-nil store")
	}
	s.Push("a", types.WebhookEvent{ID: "1"})
	got, _ := s.Recent("a", 10)
	if len(got) != 1 {
		t.Errorf("want 1, got %d", len(got))
	}
}

func TestInitStoreRedisInitError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from logger.Fatal on redis init failure")
		}
	}()
	initStore(panicOnFatalLogger(), "redis://127.0.0.1:1", 0, 100)
}

func TestLogsHandlerEncodeError(t *testing.T) {
	srv := newServer(zap.NewNop(), store.NewMemory(10))
	ch := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/logs/"+ch, nil)
	req = mux.SetURLVars(req, map[string]string{"channel": ch})
	srv.logsHandler(&failWriter{httptest.NewRecorder()}, req)
	// No assertion — coverage verifies the error branch executes.
}

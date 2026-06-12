package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/types"
)

// subscriber wraps a single WebSocket connection for a channel.
type subscriber struct {
	conn *websocket.Conn
	send chan []byte
}

// channel holds all active subscribers for a named webhook channel.
type channel struct {
	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
}

func (c *channel) add(s *subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscribers[s] = struct{}{}
}

func (c *channel) remove(s *subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subscribers, s)
}

func (c *channel) broadcast(msg []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.subscribers {
		select {
		case s.send <- msg:
		default:
			// Slow subscriber — drop the message rather than blocking.
		}
	}
	return len(c.subscribers)
}

func (c *channel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subscribers)
}

// hub manages all named channels.
type hub struct {
	mu       sync.Mutex
	channels map[string]*channel
}

func newHub() *hub {
	return &hub{channels: make(map[string]*channel)}
}

func (h *hub) getOrCreate(name string) *channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.channels[name]
	if !ok {
		ch = &channel{subscribers: make(map[*subscriber]struct{})}
		h.channels[name] = ch
	}
	return ch
}

// server wires together the HTTP handlers and the hub.
type server struct {
	hub      *hub
	upgrader websocket.Upgrader
}

func newServer() *server {
	return &server{
		hub: newHub(),
		upgrader: websocket.Upgrader{
			// Allow all origins for a local dev proxy tool.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// generateID returns 8 random hex characters suitable for request tracing.
func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hookHandler accepts an incoming webhook POST (or any method) and broadcasts
// it to all WebSocket subscribers on the named channel.
func (s *server) hookHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	channelName := vars["channel"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[webhook] error reading body: %v", err)
		http.Error(w, "failed to read body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	event := types.WebhookEvent{
		ID:      generateID(),
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: map[string][]string(r.Header),
		Body:    body,
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[webhook] error marshaling event: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ch := s.hub.getOrCreate(channelName)
	n := ch.broadcast(data)
	log.Printf("[webhook] id=%s channel=%s method=%s path=%s query=%q body_bytes=%d subscribers=%d",
		event.ID, channelName, event.Method, event.Path, event.Query, len(body), n)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// subscribeHandler upgrades the HTTP connection to WebSocket and streams
// incoming webhook events for the named channel to this subscriber.
func (s *server) subscribeHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	channelName := vars["channel"]

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error on channel=%s: %v", channelName, err)
		return
	}

	sub := &subscriber{
		conn: conn,
		send: make(chan []byte, 64),
	}

	ch := s.hub.getOrCreate(channelName)
	ch.add(sub)
	log.Printf("[ws] subscriber connected channel=%s total=%d", channelName, ch.count())

	// writePump forwards queued messages to the WebSocket connection.
	go func() {
		defer func() {
			conn.Close()
		}()
		for msg := range sub.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("[ws] write error channel=%s: %v", channelName, err)
				return
			}
		}
	}()

	// readPump discards inbound frames and detects disconnection.
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	ch.remove(sub)
	close(sub.send)
	log.Printf("[ws] subscriber disconnected channel=%s total=%d", channelName, ch.count())
}

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()

	srv := newServer()

	r := mux.NewRouter()
	r.HandleFunc("/hook/{channel}", srv.hookHandler).Methods(
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	)
	r.HandleFunc("/subscribe/{channel}", srv.subscribeHandler)

	log.Printf("[server] listening on %s", *addr)
	if err := http.ListenAndServe(*addr, r); err != nil {
		log.Fatalf("[server] fatal: %v", err)
	}
}

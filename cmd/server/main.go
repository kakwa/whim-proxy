package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/store"
	"github.com/whim-proxy/internal/types"
	"github.com/whim-proxy/internal/uuid"
	"github.com/whim-proxy/internal/web"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var version = "dev"

// subscriber wraps a single WebSocket connection for a channel.
type subscriber struct {
	conn *websocket.Conn
	send chan []byte
}

// channel holds all active subscribers for a named webhook channel.
type channel struct {
	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
	maxSubs     int // 0 = unlimited
}

func (c *channel) add(s *subscriber) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxSubs > 0 && len(c.subscribers) >= c.maxSubs {
		return fmt.Errorf("subscriber limit of %d reached", c.maxSubs)
	}
	c.subscribers[s] = struct{}{}
	return nil
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

func (c *channel) isFull() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxSubs > 0 && len(c.subscribers) >= c.maxSubs
}

// hub manages all named channels.
type hub struct {
	mu          sync.Mutex
	channels    map[string]*channel
	maxChannels int // 0 = unlimited
	maxSubs     int // passed to each new channel
}

func newHub(maxChannels, maxSubs int) *hub {
	return &hub{
		channels:    make(map[string]*channel),
		maxChannels: maxChannels,
		maxSubs:     maxSubs,
	}
}

func (h *hub) getOrCreate(name string) (*channel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.channels[name]
	if !ok {
		if h.maxChannels > 0 && len(h.channels) >= h.maxChannels {
			return nil, fmt.Errorf("service at capacity, channel creation temporarily disabled")
		}
		ch = &channel{
			subscribers: make(map[*subscriber]struct{}),
			maxSubs:     h.maxSubs,
		}
		h.channels[name] = ch
	}
	return ch, nil
}

// statusResponseWriter captures the HTTP status code for logging.
type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// loggingMiddleware logs every request with remote addr, method, path, status, and duration.
func loggingMiddleware(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("http",
			zap.String("remote", r.RemoteAddr),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", sw.status),
			zap.Duration("duration", time.Since(start).Round(time.Millisecond)),
		)
	})
}

// server wires together the HTTP handlers and the hub.
type server struct {
	hub        *hub
	upgrader   websocket.Upgrader
	logger     *zap.Logger
	eventStore store.EventStore
}

func newServer(logger *zap.Logger, eventStore store.EventStore, maxChannels, maxSubs int) *server {
	return &server{
		hub:        newHub(maxChannels, maxSubs),
		logger:     logger,
		eventStore: eventStore,
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

	if !uuid.Valid(channelName) {
		http.Error(w, "channel must be a valid UUID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("webhook body read error", zap.Error(err))
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
		s.logger.Error("webhook marshal error", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ch, err := s.hub.getOrCreate(channelName)
	if err != nil {
		s.logger.Warn("webhook channel limit", zap.String("channel", channelName), zap.Error(err))
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	n := ch.broadcast(data)

	if err := s.eventStore.Push(channelName, event); err != nil {
		s.logger.Error("event store push error", zap.Error(err))
	}

	s.logger.Info("webhook",
		zap.String("id", event.ID),
		zap.String("channel", channelName),
		zap.String("method", event.Method),
		zap.String("path", event.Path),
		zap.String("query", event.Query),
		zap.Int("body_bytes", len(body)),
		zap.Int("subscribers", n),
	)

	if n >= 1 && len(body) > 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			s.logger.Debug("webhook payload", zap.String("id", event.ID), zap.String("payload", pretty.String()))
		} else {
			s.logger.Debug("webhook payload", zap.String("id", event.ID), zap.String("payload", string(body)))
		}
	}

	w.Header().Set("X-Whim-Proxy-Server", version)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// subscribeHandler upgrades the HTTP connection to WebSocket and streams
// incoming webhook events for the named channel to this subscriber.
func (s *server) subscribeHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	channelName := vars["channel"]

	if !uuid.Valid(channelName) {
		http.Error(w, "channel must be a valid UUID", http.StatusBadRequest)
		return
	}

	ch, err := s.hub.getOrCreate(channelName)
	if err != nil {
		s.logger.Warn("subscribe channel limit", zap.String("channel", channelName), zap.Error(err))
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if ch.isFull() {
		s.logger.Warn("subscribe subscriber limit", zap.String("channel", channelName))
		http.Error(w, "subscriber limit reached", http.StatusServiceUnavailable)
		return
	}

	s.logger.Info("ws upgrade request", zap.String("channel", channelName), zap.String("remote", r.RemoteAddr))

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade error", zap.String("channel", channelName), zap.String("remote", r.RemoteAddr), zap.Error(err))
		return
	}

	sub := &subscriber{
		conn: conn,
		send: make(chan []byte, 64),
	}

	if err := ch.add(sub); err != nil {
		// Channel filled between the pre-check and the upgrade.
		s.logger.Warn("ws subscriber limit after upgrade", zap.String("channel", channelName), zap.Error(err))
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "subscriber limit reached"))
		conn.Close()
		return
	}
	s.logger.Info("ws subscriber connected",
		zap.String("channel", channelName),
		zap.String("remote", r.RemoteAddr),
		zap.String("client_version", r.Header.Get("X-Whim-Proxy-Client")),
		zap.Int("total", ch.count()),
	)

	// writePump forwards queued messages to the WebSocket connection.
	go func() {
		defer conn.Close()
		for msg := range sub.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				s.logger.Error("ws write error", zap.String("channel", channelName), zap.Error(err))
				return
			}
		}
	}()

	// readPump discards inbound frames and detects disconnection.
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.logger.Error("ws read error",
					zap.String("channel", channelName),
					zap.String("remote", r.RemoteAddr),
					zap.Error(err),
				)
			}
			break
		}
	}

	ch.remove(sub)
	close(sub.send)
	s.logger.Info("ws subscriber disconnected",
		zap.String("channel", channelName),
		zap.String("remote", r.RemoteAddr),
		zap.Int("total", ch.count()),
	)
}

// logsHandler returns the last 10 events received on the named channel.
func (s *server) logsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	channelName := vars["channel"]

	if !uuid.Valid(channelName) {
		http.Error(w, "channel must be a valid UUID", http.StatusBadRequest)
		return
	}

	events, err := s.eventStore.Recent(channelName, 10)
	if err != nil {
		s.logger.Error("logs fetch error", zap.String("channel", channelName), zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []types.WebhookEvent{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Whim-Proxy-Server", version)
	if err := json.NewEncoder(w).Encode(events); err != nil {
		s.logger.Error("logs encode error", zap.Error(err))
	}
}

func buildRouter(logger *zap.Logger, srv *server) http.Handler {
	r := mux.NewRouter()
	web.RegisterHandlers(r, version)
	r.HandleFunc("/hook/{channel}", srv.hookHandler).Methods(
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	)
	r.HandleFunc("/subscribe/{channel}", srv.subscribeHandler)
	r.HandleFunc("/logs/{channel}", srv.logsHandler).Methods(http.MethodGet)
	return loggingMiddleware(logger, r)
}

func buildLogger(levelStr string, jsonFormat bool) (*zap.Logger, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(levelStr)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", levelStr, err)
	}
	var cfg zap.Config
	if jsonFormat {
		cfg = zap.NewProductionConfig()
	} else {
		cfg = zap.NewDevelopmentConfig()
	}
	cfg.Level = zap.NewAtomicLevelAt(level)
	return cfg.Build()
}

func initStore(logger *zap.Logger, redisURL string, redisTTL time.Duration, backlogSize int) store.EventStore {
	if redisURL != "" {
		rs, err := store.NewRedis(redisURL, redisTTL, backlogSize)
		if err != nil {
			logger.Fatal("redis init failed", zap.Error(err))
		}
		logger.Info("using Redis store", zap.String("url", redisURL), zap.Duration("ttl", redisTTL))
		return rs
	}
	logger.Info("using in-memory store", zap.Int("backlog_size", backlogSize))
	return store.NewMemory(backlogSize)
}

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	jsonLog := flag.Bool("json", false, "output logs in JSON format")
	backlogSize := flag.Int("backlog-size", 10000, "max events kept in the in-memory store (global across all channels)")
	redisURL := flag.String("redis-url", "", "Redis URL (redis://...) — enables Redis store instead of in-memory")
	redisTTL := flag.Duration("redis-ttl", 24*time.Hour, "TTL applied to each Redis channel key after its last write (0 = no expiry)")
	maxChannels := flag.Int("max-channels", 100000, "max number of distinct channels tracked (0 = unlimited)")
	maxClients := flag.Int("max-clients", 100, "max WebSocket subscribers per channel (0 = unlimited)")
	flag.Parse()

	logger, err := buildLogger(*logLevel, *jsonLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	srv := newServer(logger, initStore(logger, *redisURL, *redisTTL, *backlogSize), *maxChannels, *maxClients)
	logger.Info("server listening", zap.String("addr", *addr))
	if err := http.ListenAndServe(*addr, buildRouter(logger, srv)); err != nil {
		logger.Fatal("server fatal", zap.Error(err))
	}
}

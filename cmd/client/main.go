package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/types"
)

const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second
)

// replay forwards a WebhookEvent as a real HTTP request to target and logs
// the response status.
func replay(event types.WebhookEvent, target string) {
	// Build the destination URL: target + original path + original query.
	destURL := strings.TrimRight(target, "/") + event.Path
	if event.Query != "" {
		destURL += "?" + event.Query
	}

	req, err := http.NewRequest(event.Method, destURL, bytes.NewReader(event.Body))
	if err != nil {
		log.Printf("[replay] id=%s error building request: %v", event.ID, err)
		return
	}

	// Copy all original headers.
	for key, values := range event.Headers {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[replay] id=%s error forwarding to %s: %v", event.ID, destURL, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	log.Printf("[replay] id=%s method=%s url=%s -> %d", event.ID, event.Method, destURL, resp.StatusCode)
}

// connect establishes a WebSocket connection and reads events until an error
// occurs. Returns the error so the caller can decide whether to reconnect.
func connect(wsURL string, target string) error {
	log.Printf("[client] connecting to %s", wsURL)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("dial: %w (server returned HTTP %d: %s)", err, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	log.Printf("[client] connected, waiting for events (target=%s)", target)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var event types.WebhookEvent
		if err := json.Unmarshal(msg, &event); err != nil {
			log.Printf("[client] failed to unmarshal event: %v", err)
			continue
		}

		go replay(event, target)
	}
}

func main() {
	server := flag.String("server", "ws://localhost:9000", "WebSocket server address")
	channel := flag.String("channel", "", "channel name to subscribe to (required)")
	target := flag.String("target", "http://localhost:8080", "local target to forward requests to")
	flag.Parse()

	if *channel == "" {
		log.Fatal("--channel is required")
	}

	// Construct the full WebSocket subscription URL.
	base, err := url.Parse(*server)
	if err != nil {
		log.Fatalf("invalid --server URL: %v", err)
	}
	base.Path = "/subscribe/" + *channel
	wsURL := base.String()

	backoff := initialBackoff

	for {
		err := connect(wsURL, *target)
		if err != nil {
			log.Printf("[client] connection error: %v — reconnecting in %s", err, backoff)
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

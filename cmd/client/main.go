package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/whim-proxy/internal/types"
	"github.com/whim-proxy/internal/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var version = "dev"

const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second
)

// replay forwards a WebhookEvent as a real HTTP request to target and logs
// the response status.
func replay(logger *zap.Logger, event types.WebhookEvent, target string) {
	destURL := strings.TrimRight(target, "/") + event.Path
	if event.Query != "" {
		destURL += "?" + event.Query
	}

	req, err := http.NewRequest(event.Method, destURL, bytes.NewReader(event.Body))
	if err != nil {
		logger.Error("replay build request error", zap.String("id", event.ID), zap.Error(err))
		return
	}

	for key, values := range event.Headers {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	req.Header.Set("X-Whim-Proxy-Client", version)

	logger.Debug("replay event",
		zap.String("id", event.ID),
		zap.String("method", event.Method),
		zap.String("url", destURL),
		zap.Any("headers", event.Headers),
		zap.ByteString("body", event.Body),
	)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error("replay forward error", zap.String("id", event.ID), zap.String("url", destURL), zap.Error(err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	logger.Info("replay",
		zap.String("id", event.ID),
		zap.String("method", event.Method),
		zap.String("url", destURL),
		zap.Int("status", resp.StatusCode),
	)
}

// connect establishes a WebSocket connection and reads events until an error
// occurs. Returns the error so the caller can decide whether to reconnect.
func connect(logger *zap.Logger, wsURL string, target string) error {
	logger.Info("client connecting", zap.String("url", wsURL))

	header := http.Header{}
	header.Set("X-Whim-Proxy-Client", version)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			reason := strings.TrimSpace(string(body))
			logger.Error("server rejected connection",
				zap.Int("status", resp.StatusCode),
				zap.String("reason", reason),
			)
			return fmt.Errorf("dial: %w (HTTP %d: %s)", err, resp.StatusCode, reason)
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	logger.Info("client connected", zap.String("target", target))

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var event types.WebhookEvent
		if err := json.Unmarshal(msg, &event); err != nil {
			logger.Error("unmarshal event error", zap.Error(err))
			continue
		}

		go replay(logger, event, target)
	}
}

// fetchLogs retrieves the last events for channel from the server and writes
// them as pretty-printed JSON to out. client is used for the HTTP request so
// callers can inject test transports.
func fetchLogs(client *http.Client, wsServer string, channel string, out io.Writer) error {
	base, err := url.Parse(wsServer)
	if err != nil {
		return fmt.Errorf("parse server URL: %w", err)
	}
	switch base.Scheme {
	case "ws":
		base.Scheme = "http"
	case "wss":
		base.Scheme = "https"
	}
	base.Path = "/logs/" + channel

	resp, err := client.Get(base.String())
	if err != nil {
		return fmt.Errorf("fetch logs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var events []types.WebhookEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return fmt.Errorf("decode logs: %w", err)
	}

	data, _ := json.MarshalIndent(events, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
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

func main() {
	server := flag.String("server", "", "WebSocket server address (required)")
	channel := flag.String("channel", "", "channel name to subscribe to (required)")
	target := flag.String("target", "http://localhost:8080", "local target to forward requests to")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	jsonLog := flag.Bool("json", false, "output logs in JSON format")
	genUUID := flag.Bool("gen-uuid", false, "print a new UUID to stdout and exit")
	logsFlag := flag.Bool("logs", false, "fetch and print the last received events for the channel, then exit")
	flag.Parse()

	if *genUUID {
		fmt.Println(uuid.New())
		return
	}

	logger, err := buildLogger(*logLevel, *jsonLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	if *server == "" {
		logger.Fatal("--server is required")
	}
	if *channel == "" {
		logger.Fatal("--channel is required")
	}
	if !uuid.Valid(*channel) {
		logger.Fatal("--channel must be a valid UUID")
	}

	if *logsFlag {
		if err := fetchLogs(http.DefaultClient, *server, *channel, os.Stdout); err != nil {
			logger.Fatal("fetch logs failed", zap.Error(err))
		}
		return
	}

	base, err := url.Parse(*server)
	if err != nil {
		logger.Fatal("invalid --server URL", zap.Error(err))
	}
	base.Path = "/subscribe/" + *channel
	wsURL := base.String()

	backoff := initialBackoff

	for {
		err := connect(logger, wsURL, *target)
		if err != nil {
			logger.Warn("connection error, reconnecting",
				zap.Error(err),
				zap.Duration("backoff", backoff),
			)
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

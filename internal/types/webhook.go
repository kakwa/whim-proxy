package types

// WebhookEvent represents a captured incoming webhook request,
// serialized over the WebSocket wire to subscribers.
type WebhookEvent struct {
	ID      string              `json:"id"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

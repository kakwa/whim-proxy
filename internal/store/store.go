package store

import (
	"sync"

	"github.com/whim-proxy/internal/types"
)

// EventStore persists and retrieves webhook events per channel.
type EventStore interface {
	Push(channel string, event types.WebhookEvent) error
	Recent(channel string, n int) ([]types.WebhookEvent, error)
}

type memEntry struct {
	channel string
	event   types.WebhookEvent
}

type memoryStore struct {
	mu    sync.RWMutex
	buf   []memEntry
	head  int
	count int
}

// NewMemory returns an in-memory EventStore with a fixed global capacity.
// When full, the oldest event across all channels is silently overwritten.
func NewMemory(capacity int) EventStore {
	return &memoryStore{buf: make([]memEntry, capacity)}
}

func (m *memoryStore) Push(channel string, event types.WebhookEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cap := len(m.buf)
	m.buf[m.head] = memEntry{channel: channel, event: event}
	m.head = (m.head + 1) % cap
	if m.count < cap {
		m.count++
	}
	return nil
}

func (m *memoryStore) Recent(channel string, n int) ([]types.WebhookEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cap := len(m.buf)
	out := make([]types.WebhookEvent, 0, n)
	for i := 1; i <= m.count && len(out) < n; i++ {
		idx := (m.head - i + cap) % cap
		if m.buf[idx].channel == channel {
			out = append(out, m.buf[idx].event)
		}
	}
	// Reverse to chronological order (oldest first).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

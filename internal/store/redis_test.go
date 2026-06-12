package store

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/whim-proxy/internal/types"
)

func ev2(id string) types.WebhookEvent {
	return types.WebhookEvent{ID: id, Method: "POST", Path: "/hook/test"}
}

func TestNewRedisInvalidURL(t *testing.T) {
	_, err := NewRedis("not-a-url", 0, 100)
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestNewRedisPingFail(t *testing.T) {
	_, err := NewRedis("redis://127.0.0.1:1", 0, 100)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestRedisPushAndRecent(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	s.Push("a", ev2("1"))
	s.Push("a", ev2("2"))
	s.Push("b", ev2("3"))

	got, err := s.Recent("a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// chronological order: oldest first
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("order: got [%s %s], want [1 2]", got[0].ID, got[1].ID)
	}
}

func TestRedisRecentLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		s.Push("a", ev2(string(rune('a'+i))))
	}
	got, _ := s.Recent("a", 5)
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
}

func TestRedisStoreCapacity(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := NewRedis("redis://"+mr.Addr(), 0, 3)
	if err != nil {
		t.Fatal(err)
	}

	s.Push("a", ev2("1"))
	s.Push("a", ev2("2"))
	s.Push("a", ev2("3"))
	s.Push("a", ev2("4")) // evicts "1"

	got, _ := s.Recent("a", 10)
	if len(got) != 3 {
		t.Fatalf("want 3 (capped), got %d", len(got))
	}
	if got[0].ID != "2" {
		t.Errorf("first: got %q, want \"2\"", got[0].ID)
	}
}

func TestRedisStoreEmptyChannel(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Recent("nonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want 0, got %d", len(got))
	}
}

func TestRedisStoreTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := NewRedis("redis://"+mr.Addr(), time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}

	s.Push("a", ev2("1"))
	mr.FastForward(59 * time.Second)

	got, _ := s.Recent("a", 10)
	if len(got) != 1 {
		t.Errorf("before expiry: want 1, got %d", len(got))
	}

	mr.FastForward(2 * time.Second)

	got, _ = s.Recent("a", 10)
	if len(got) != 0 {
		t.Errorf("after expiry: want 0, got %d", len(got))
	}
}

func TestRedisStoreNoTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	// ttl=0 means no expiry — key should survive FastForward
	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	s.Push("a", ev2("1"))
	mr.FastForward(24 * time.Hour)

	got, _ := s.Recent("a", 10)
	if len(got) != 1 {
		t.Errorf("no-TTL key expired unexpectedly, got %d events", len(got))
	}
}

func TestRedisRecentInvalidJSON(t *testing.T) {
	mr := miniredis.RunT(t)
	// Inject invalid JSON directly so the unmarshal-error continue branch is hit.
	mr.Lpush("whim:logs:chan1", `{"id":"1","method":"POST","path":"/","query":"","headers":null,"body":null}`)
	mr.Lpush("whim:logs:chan1", "not-json")

	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Recent("chan1", 10)
	if err != nil {
		t.Fatal(err)
	}
	// "not-json" is skipped; only the valid entry is returned.
	if len(got) != 1 {
		t.Errorf("want 1, got %d", len(got))
	}
}

func TestRedisRecentLRangeError(t *testing.T) {
	mr := miniredis.RunT(t)
	// SET a string key with the channel name so LRANGE returns WRONGTYPE.
	mr.Set("whim:logs:badchan", "not-a-list")

	s, err := NewRedis("redis://"+mr.Addr(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Recent("badchan", 10)
	if err == nil {
		t.Error("expected WRONGTYPE error from LRange on a string key")
	}
}

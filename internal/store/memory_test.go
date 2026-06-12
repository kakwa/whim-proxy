package store

import (
	"fmt"
	"testing"

	"github.com/whim-proxy/internal/types"
)

func ev(id string) types.WebhookEvent {
	return types.WebhookEvent{ID: id, Method: "POST", Path: "/hook/test"}
}

func TestMemoryPushAndRecent(t *testing.T) {
	s := NewMemory(100)
	s.Push("a", ev("1"))
	s.Push("a", ev("2"))
	s.Push("b", ev("3"))

	got, err := s.Recent("a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("order: got [%s %s], want [1 2]", got[0].ID, got[1].ID)
	}
}

func TestMemoryRecentLimit(t *testing.T) {
	s := NewMemory(100)
	for i := 0; i < 20; i++ {
		s.Push("a", ev(fmt.Sprintf("%d", i)))
	}
	got, _ := s.Recent("a", 5)
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	if got[len(got)-1].ID != "19" {
		t.Errorf("last: got %q, want \"19\"", got[len(got)-1].ID)
	}
}

func TestMemoryGlobalCap(t *testing.T) {
	s := NewMemory(5)
	for i := 0; i < 10; i++ {
		s.Push("a", ev(fmt.Sprintf("%d", i)))
	}
	got, _ := s.Recent("a", 10)
	if len(got) != 5 {
		t.Fatalf("want 5 (capped), got %d", len(got))
	}
	if got[0].ID != "5" {
		t.Errorf("first: got %q, want \"5\"", got[0].ID)
	}
}

func TestMemoryChannelIsolation(t *testing.T) {
	s := NewMemory(100)
	s.Push("a", ev("a1"))
	s.Push("b", ev("b1"))
	s.Push("a", ev("a2"))

	gotA, _ := s.Recent("a", 10)
	gotB, _ := s.Recent("b", 10)
	if len(gotA) != 2 || len(gotB) != 1 {
		t.Errorf("isolation: a=%d b=%d", len(gotA), len(gotB))
	}
}

func TestMemoryEmptyChannel(t *testing.T) {
	s := NewMemory(100)
	got, err := s.Recent("nonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want 0, got %d", len(got))
	}
}

func TestMemoryGlobalCapEvictsAcrossChannels(t *testing.T) {
	// cap=3; push a1, b1, a2 then push b2 — a1 is evicted
	s := NewMemory(3)
	s.Push("a", ev("a1"))
	s.Push("b", ev("b1"))
	s.Push("a", ev("a2"))
	s.Push("b", ev("b2")) // evicts a1

	gotA, _ := s.Recent("a", 10)
	if len(gotA) != 1 || gotA[0].ID != "a2" {
		t.Errorf("after eviction: a=%v", gotA)
	}
}

package uuid

import "testing"

func TestNewIsValid(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := New()
		if !Valid(id) {
			t.Fatalf("generated UUID %q failed validation", id)
		}
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"550E8400-E29B-41D4-A716-446655440000", true},
		{"not-a-uuid", false},
		{"", false},
	}
	for _, c := range cases {
		if got := Valid(c.in); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

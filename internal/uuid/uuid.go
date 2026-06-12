package uuid

import "github.com/google/uuid"

// New generates a random UUID v4.
func New() string {
	return uuid.New().String()
}

// Valid reports whether s is a valid UUID.
func Valid(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

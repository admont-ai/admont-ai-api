package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchIdentity(t *testing.T) {
	tests := []struct {
		name     string
		stored   string
		incoming string
		want     bool
	}{
		{"exact match with provider", "google:alice@example.com", "google:alice@example.com", true},
		{"exact match bare email", "alice@example.com", "alice@example.com", true},
		{"bare stored matches prefixed incoming", "alice@example.com", "google:alice@example.com", true},
		{"bare stored matches different provider", "alice@example.com", "github:alice@example.com", true},
		{"prefixed stored does not match different provider", "google:alice@example.com", "github:alice@example.com", false},
		{"prefixed stored does not match bare email", "google:alice@example.com", "alice@example.com", false},
		{"different emails", "alice@example.com", "bob@example.com", false},
		{"different emails with provider", "google:alice@example.com", "google:bob@example.com", false},
		{"empty stored", "", "", true},
		{"empty stored vs non-empty", "", "google:alice@example.com", false},
		{"non-empty stored vs empty", "alice@example.com", "", false},
		{"internal provider", "internal:admin@example.com", "internal:admin@example.com", true},
		{"bare vs internal", "admin@example.com", "internal:admin@example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchIdentity(tt.stored, tt.incoming))
		})
	}
}

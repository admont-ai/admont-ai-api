package store

import "testing"

func TestIsValidIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"simple", "admont-ai", true},
		{"underscore", "admont_ai", true},
		{"alphanumeric", "db123", true},
		{"empty", "", false},
		{"sql injection", `x"; DROP DATABASE postgres; --`, false},
		{"space", "my db", false},
		{"quote", `db"name`, false},
		{"semicolon", "db;drop", false},
		{"backslash", `db\name`, false},
		{"too long", string(make([]byte, 64)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidIdentifier(tt.in); got != tt.want {
				t.Errorf("isValidIdentifier(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

package auth

import "strings"

// MatchIdentity compares a stored identity against an incoming one.
// Supports backward compatibility: a stored bare email matches "provider:email".
func MatchIdentity(stored, incoming string) bool {
	if stored == incoming {
		return true
	}
	// If stored has no provider prefix, match against the email part of incoming.
	if !strings.Contains(stored, ":") {
		_, email, _ := strings.Cut(incoming, ":")
		return stored == email
	}
	return false
}

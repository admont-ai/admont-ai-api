package auth

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// MatchIdentity compares a stored identity against an incoming one.
// Supports backward compatibility: a stored bare email matches "provider:email".
// Deprecated: bare email matching will be removed in a future version.
// Use fully qualified identities (e.g. "google:user@example.com") in admin lists.
func MatchIdentity(stored, incoming string) bool {
	if stored == incoming {
		return true
	}
	// If stored has no provider prefix, match against the email part of incoming.
	if !strings.Contains(stored, ":") {
		_, email, _ := strings.Cut(incoming, ":")
		if stored == email {
			log.WithField("stored", stored).WithField("incoming", incoming).
				Warn("bare email identity match is deprecated — use provider:email format (e.g. google:user@example.com)")
			return true
		}
	}
	return false
}

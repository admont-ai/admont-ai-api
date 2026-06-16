package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestConfigRedacted(t *testing.T) {
	c := Config{Hostname: "host", JWTSecret: "jwt", EncryptionKey: "enc"}
	c.Database.Password = "pw"

	r := c.Redacted()
	if r.JWTSecret != "[REDACTED]" || r.EncryptionKey != "[REDACTED]" || r.Database.Password != "[REDACTED]" {
		t.Fatalf("secrets not redacted: %+v", r)
	}
	if r.Hostname != "host" {
		t.Errorf("non-secret field changed: %q", r.Hostname)
	}

	// Original must not be mutated.
	if c.JWTSecret != "jwt" || c.EncryptionKey != "enc" || c.Database.Password != "pw" {
		t.Fatal("Redacted mutated the original config")
	}

	// Empty secrets stay empty (so logs distinguish unset from set).
	if (Config{}).Redacted().JWTSecret != "" {
		t.Error("empty secret should remain empty")
	}

	// The rendered log string must not contain the real secret values.
	out := fmt.Sprintf("%+v", c.Redacted())
	for _, secret := range []string{"jwt", "enc", "pw"} {
		if strings.Contains(out, secret) {
			t.Errorf("redacted output leaked secret %q: %s", secret, out)
		}
	}
}

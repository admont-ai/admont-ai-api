package auth

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/publicurl"

	"github.com/stretchr/testify/assert"
)

func TestIsAllowedRedirect(t *testing.T) {
	h := NewHandler(NewRegistry(), NewJWTService("secret", time.Hour), []string{
		"http://localhost:5173",
		"https://app.example.com",
	}, nil, nil, "manual")

	tests := []struct {
		name    string
		url     string
		allowed bool
	}{
		{"allowed localhost", "http://localhost:5173/callback", true},
		{"allowed with path", "https://app.example.com/auth/done?foo=bar", true},
		{"different port rejected", "http://localhost:8080/callback", false},
		{"evil domain rejected", "https://evil.com/steal?token=x", false},
		{"scheme mismatch rejected", "http://app.example.com/callback", false},
		{"empty rejected", "", false},
		{"relative path rejected", "/callback", false},
		{"subdomain rejected", "https://sub.app.example.com/callback", false},
		{"case insensitive", "HTTP://LOCALHOST:5173/callback", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.allowed, h.isAllowedRedirect(nil, tt.url))
		})
	}
}

func TestIsAllowedRedirect_SameOrigin(t *testing.T) {
	h := NewHandler(NewRegistry(), NewJWTService("secret", time.Hour), nil, nil, nil, "manual")
	h.SetPublicURL(publicurl.New("", []string{"10.0.0.0/8"}))

	req := httptest.NewRequest("GET", "/auth/login", nil)
	req.RemoteAddr = "10.1.1.1:1234"
	req.Host = "docs.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Prefix", "/api")

	assert.True(t, h.isAllowedRedirect(req, "https://docs.example.com/auth/callback"))
	assert.False(t, h.isAllowedRedirect(req, "http://docs.example.com/auth/callback"))
	assert.False(t, h.isAllowedRedirect(req, "https://evil.example.com/x"))
	assert.False(t, h.isAllowedRedirect(nil, "https://docs.example.com/x"))
}

func TestExchange_ValidCode(t *testing.T) {
	h := NewHandler(NewRegistry(), NewJWTService("secret", time.Hour), nil, nil, nil, "manual")

	h.authCodes.Store("test-code", &authCodeStore{
		Token:        "jwt-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(30 * time.Second),
	})

	val, ok := h.authCodes.Load("test-code")
	assert.True(t, ok)
	entry := val.(*authCodeStore)
	assert.Equal(t, "jwt-token", entry.Token)

	// Code should be single-use
	h.authCodes.Delete("test-code")
	_, ok = h.authCodes.Load("test-code")
	assert.False(t, ok)
}

func TestExchange_ExpiredCode(t *testing.T) {
	h := NewHandler(NewRegistry(), NewJWTService("secret", time.Hour), nil, nil, nil, "manual")

	h.authCodes.Store("expired-code", &authCodeStore{
		Token:        "jwt-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-10 * time.Second),
	})

	val, ok := h.authCodes.Load("expired-code")
	assert.True(t, ok)
	entry := val.(*authCodeStore)
	assert.True(t, time.Now().After(entry.ExpiresAt))
}

func TestSplitName(t *testing.T) {
	cases := []struct{ in, first, last string }{
		{"", "", ""},
		{"Alice", "Alice", ""},
		{"Alice Smith", "Alice", "Smith"},
		{"Alice van der Berg", "Alice", "van der Berg"},
		{"  Bob  Jones ", "Bob", "Jones"},
	}
	for _, c := range cases {
		f, l := splitName(c.in)
		if f != c.first || l != c.last {
			t.Errorf("splitName(%q) = (%q,%q), want (%q,%q)", c.in, f, l, c.first, c.last)
		}
	}
}

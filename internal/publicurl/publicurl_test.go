package publicurl

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStaticBaseURLWins(t *testing.T) {
	r := New("https://api.example.com/base/", []string{"10.0.0.0/8"})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	req.Host = "other.example.com"
	req.Header.Set("X-Forwarded-Prefix", "/api")
	assert.Equal(t, "https://api.example.com/base", r.BaseURL(req))
	assert.Equal(t, "https://api.example.com", r.Origin(req))
	assert.Equal(t, "api.example.com", r.Host(req))
	assert.Equal(t, "/base", r.Prefix(req))
}

func TestDerivedFromTrustedProxy(t *testing.T) {
	r := New("", []string{"10.0.0.0/8"})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.120.1.2:5555"
	req.Host = "docs.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "docs.example.com")
	req.Header.Set("X-Forwarded-Prefix", "/api")
	assert.Equal(t, "https://docs.example.com/api", r.BaseURL(req))
	assert.Equal(t, "docs.example.com", r.Host(req))
}

func TestPrefixUsesLastValue(t *testing.T) {
	r := New("", []string{"10.0.0.0/8"})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:1"
	req.Header.Add("X-Forwarded-Prefix", "/evil")
	req.Header.Add("X-Forwarded-Prefix", "/api")
	assert.Equal(t, "/api", r.Prefix(req))
}

func TestForwardedHeadersIgnoredFromUntrustedPeer(t *testing.T) {
	r := New("", []string{"10.0.0.0/8"})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.Host = "localhost:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Prefix", "/api")
	assert.Equal(t, "http://localhost:8080", r.BaseURL(req))
	assert.Equal(t, "localhost", r.Host(req))
}

func TestInvalidPrefixAndHostRejected(t *testing.T) {
	r := New("", []string{"10.0.0.1"})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:1"
	req.Host = "ok.example.com"
	req.Header.Set("X-Forwarded-Host", "bad host/<x>")
	req.Header.Set("X-Forwarded-Prefix", "/a/../b\"")
	assert.Equal(t, "http://ok.example.com", r.BaseURL(req))
}

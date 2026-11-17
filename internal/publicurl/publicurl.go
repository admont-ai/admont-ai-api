// Package publicurl resolves the public base URL under which the API is reached.
//
// The API can be served on several hosts and/or behind a path prefix (e.g.
// https://docs.example.com/api, with a reverse proxy stripping /api). URLs the
// API hands out — OAuth callbacks, MCP OAuth metadata, the SSE message
// endpoint, the passkey relying party — must point back to the host/prefix
// the caller actually used.
//
// When a static base URL is configured (AUTH_BASE_URL) it is always used.
// Otherwise the base URL is derived per request from the Host header and, for
// requests coming from a trusted proxy (TRUSTED_PROXIES), from the
// X-Forwarded-Proto, X-Forwarded-Host and X-Forwarded-Prefix headers.
package publicurl

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Resolver derives the public base URL for a request.
type Resolver struct {
	static  string
	trusted []*net.IPNet
}

// New builds a Resolver. staticBaseURL may be empty (derive per request).
// trustedProxies are IPs or CIDRs whose X-Forwarded-* headers are honored.
func New(staticBaseURL string, trustedProxies []string) *Resolver {
	r := &Resolver{static: strings.TrimRight(staticBaseURL, "/")}
	for _, p := range trustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if ip := net.ParseIP(p); ip != nil && ip.To4() != nil {
				p += "/32"
			} else {
				p += "/128"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			log.WithField("proxy", p).Warn("publicurl: ignoring invalid trusted proxy")
			continue
		}
		r.trusted = append(r.trusted, n)
	}
	return r
}

// Static reports whether a fixed base URL is configured.
func (r *Resolver) Static() bool { return r != nil && r.static != "" }

// BaseURL returns the public base URL (scheme://host[/prefix], no trailing
// slash) for the request.
func (r *Resolver) BaseURL(req *http.Request) string {
	if r.Static() {
		return r.static
	}
	return r.Origin(req) + r.Prefix(req)
}

// Origin returns scheme://host for the request.
func (r *Resolver) Origin(req *http.Request) string {
	if r.Static() {
		if u, err := url.Parse(r.static); err == nil {
			return u.Scheme + "://" + u.Host
		}
	}
	return r.scheme(req) + "://" + r.host(req)
}

// Host returns the public host (without port) for the request.
func (r *Resolver) Host(req *http.Request) string {
	if r.Static() {
		if u, err := url.Parse(r.static); err == nil {
			return u.Hostname()
		}
	}
	h := r.host(req)
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.Trim(host, "[]")
	}
	return h
}

// Prefix returns the path prefix the proxy stripped ("" if none).
func (r *Resolver) Prefix(req *http.Request) string {
	if r.Static() {
		if u, err := url.Parse(r.static); err == nil {
			return strings.TrimRight(u.Path, "/")
		}
		return ""
	}
	if !r.fromTrustedProxy(req) {
		return ""
	}
	// Proxies append their stripped prefix; use the last value so a
	// client-supplied header cannot take precedence.
	vals := req.Header.Values("X-Forwarded-Prefix")
	if len(vals) == 0 {
		return ""
	}
	parts := strings.Split(vals[len(vals)-1], ",")
	p := strings.TrimRight(strings.TrimSpace(parts[len(parts)-1]), "/")
	if !validPrefix.MatchString(p) {
		return ""
	}
	return p
}

var (
	validPrefix = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)
	validHost   = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)
)

func (r *Resolver) scheme(req *http.Request) string {
	if r.fromTrustedProxy(req) {
		if p := strings.ToLower(firstValue(req.Header.Get("X-Forwarded-Proto"))); p == "http" || p == "https" {
			return p
		}
	}
	if req.TLS != nil {
		return "https"
	}
	return "http"
}

func (r *Resolver) host(req *http.Request) string {
	if r.fromTrustedProxy(req) {
		if h := firstValue(req.Header.Get("X-Forwarded-Host")); h != "" && validHost.MatchString(h) {
			return h
		}
	}
	if validHost.MatchString(req.Host) {
		return req.Host
	}
	return "localhost"
}

func (r *Resolver) fromTrustedProxy(req *http.Request) bool {
	if len(r.trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(req.RemoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func firstValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

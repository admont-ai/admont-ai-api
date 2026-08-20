package mcp

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRoutedTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s := NewServer(
		map[string]repo.RepoBackend{},
		map[string]*draft.Manager{},
		map[string]string{},
		map[string]*git_repo.GitRepo{},
		&sync.Map{},
		map[string]*permissions.Resolver{},
		auth.NewJWTService("test-secret", time.Hour),
		auth.NewRegistry(),
		"http://localhost:8080",
	)

	r := gin.New()
	s.RegisterRoutes(r)
	return s, r
}

// TestStreamableEndpointMountsAlongsideSSE confirms the new /mcp streamable
// HTTP transport (both with and without a trailing slash) is reachable and
// doesn't collide with the existing SSE/OAuth routes on the same group.
func TestStreamableEndpointMountsAlongsideSSE(t *testing.T) {
	_, r := newRoutedTestServer(t)

	for _, path := range []string{"/mcp", "/mcp/"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			// No bearer token supplied — must be rejected by requireAuth (401),
			// not 404 (route missing) or 405 (method not routed here at all).
			assert.Equal(t, 401, w.Code, "expected 401 for unauthenticated streamable request to %s", path)
		})
	}

	// Existing SSE endpoint must still be reachable and still auth-gated.
	req := httptest.NewRequest("GET", "/mcp/sse", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)

	// Existing OAuth endpoints must still be reachable (unauthenticated by design).
	req = httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
}

func TestProtectedResourceMetadata_MatchesPreviousShape(t *testing.T) {
	_, r := newRoutedTestServer(t)

	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"resource":"http://localhost:8080"`)
	assert.Contains(t, body, `"authorization_servers":["http://localhost:8080"]`)
	assert.Contains(t, body, `"bearer_methods_supported":["header"]`)
}

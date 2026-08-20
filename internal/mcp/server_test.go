package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
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
	"golang.org/x/oauth2"
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

// --- OAuth dynamic client registration ---

func TestOAuthRegister_ValidBody(t *testing.T) {
	_, r := newRoutedTestServer(t)

	body := `{"redirect_uris":["https://client.example/callback"],"client_name":"Test Client"}`
	req := httptest.NewRequest("POST", "/mcp/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 201, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["client_id"])
	assert.Equal(t, "none", resp["token_endpoint_auth_method"])
	assert.Equal(t, "Test Client", resp["client_name"])
}

func TestOAuthRegister_InvalidBody(t *testing.T) {
	_, r := newRoutedTestServer(t)

	req := httptest.NewRequest("POST", "/mcp/register", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

// TestOAuthRegister_NoRedirectURIsAllowsAny documents existing, permissive
// behavior: a client registered without redirect_uris is later accepted with
// ANY redirect_uri at /mcp/authorize (validateClientRedirectURI's
// len(client.RedirectURIs) == 0 fallback).
func TestOAuthRegister_NoRedirectURIsAllowsAny(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.registry.Register(fakeProviderEntry("google"))

	req := httptest.NewRequest("POST", "/mcp/register", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 201, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	clientID := resp["client_id"].(string)

	// Any redirect_uri is accepted for a client with no registered list.
	req2 := httptest.NewRequest("GET", "/mcp/authorize?client_id="+clientID+"&redirect_uri=https://anything.example/cb&code_challenge=abc", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, 307, w2.Code, "expected the auth flow to proceed rather than reject the redirect_uri")
}

// --- oauthAuthorize provider selection ---

func fakeProviderEntry(name string) *auth.ProviderEntry {
	return &auth.ProviderEntry{
		Name: name,
		OAuthConfig: &oauth2.Config{
			ClientID:    "fake-client-id",
			RedirectURL: "http://localhost:8080/mcp/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://" + name + ".example/auth", TokenURL: "https://" + name + ".example/token"},
		},
	}
}

func TestOAuthAuthorize_NoProvidersConfigured(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.registeredClients.Store("client1", &mcpRegisteredClient{RedirectURIs: []string{"https://client.example/cb"}})

	req := httptest.NewRequest("GET", "/mcp/authorize?client_id=client1&redirect_uri=https://client.example/cb&code_challenge=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 500, w.Code)
}

func TestOAuthAuthorize_MissingRedirectURIOrChallenge(t *testing.T) {
	_, r := newRoutedTestServer(t)

	for _, q := range []string{"", "redirect_uri=https://client.example/cb", "code_challenge=abc"} {
		req := httptest.NewRequest("GET", "/mcp/authorize?"+q, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, 400, w.Code, "query %q", q)
	}
}

func TestOAuthAuthorize_UnregisteredRedirectURI(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.registeredClients.Store("client1", &mcpRegisteredClient{RedirectURIs: []string{"https://allowed.example/cb"}})

	req := httptest.NewRequest("GET", "/mcp/authorize?client_id=client1&redirect_uri=https://not-allowed.example/cb&code_challenge=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthAuthorize_SingleProviderRedirectsDirectly(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.registry.Register(fakeProviderEntry("google"))
	s.registeredClients.Store("client1", &mcpRegisteredClient{RedirectURIs: []string{"https://client.example/cb"}})

	req := httptest.NewRequest("GET", "/mcp/authorize?client_id=client1&redirect_uri=https://client.example/cb&code_challenge=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 307, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "google.example/auth")
}

func TestOAuthAuthorize_MultipleProvidersShowsChooser(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.registry.Register(fakeProviderEntry("google"))
	s.registry.Register(fakeProviderEntry("github"))
	s.registeredClients.Store("client1", &mcpRegisteredClient{RedirectURIs: []string{"https://client.example/cb"}})

	req := httptest.NewRequest("GET", "/mcp/authorize?client_id=client1&redirect_uri=https://client.example/cb&code_challenge=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "provider=google")
	assert.Contains(t, body, "provider=github")
}

func TestOAuthAuthorize_ExplicitInternalProvider(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.authn = &auth.Authenticator{}
	s.registeredClients.Store("client1", &mcpRegisteredClient{RedirectURIs: []string{"https://client.example/cb"}})

	req := httptest.NewRequest("GET", "/mcp/authorize?client_id=client1&redirect_uri=https://client.example/cb&code_challenge=abc&provider=internal", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "Sign In")
}

// --- oauthCallback ---

func TestOAuthCallback_MissingParams(t *testing.T) {
	_, r := newRoutedTestServer(t)
	req := httptest.NewRequest("GET", "/mcp/callback", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthCallback_UnknownState(t *testing.T) {
	_, r := newRoutedTestServer(t)
	req := httptest.NewRequest("GET", "/mcp/callback?code=abc&state=unknown", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthCallback_ExpiredState(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.authStates.Store("state123", &mcpAuthState{
		ClientRedirectURI: "https://client.example/cb",
		CreatedAt:         time.Now().Add(-11 * time.Minute),
	})

	req := httptest.NewRequest("GET", "/mcp/callback?code=abc&state=state123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

// --- oauthToken / PKCE ---

func TestOAuthToken_UnsupportedGrantType(t *testing.T) {
	_, r := newRoutedTestServer(t)
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(url.Values{"grant_type": {"password"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthToken_MissingCode(t *testing.T) {
	_, r := newRoutedTestServer(t)
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(url.Values{"grant_type": {"authorization_code"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthToken_UnknownCode(t *testing.T) {
	_, r := newRoutedTestServer(t)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"does-not-exist"}}
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthToken_ExpiredCode(t *testing.T) {
	s, r := newRoutedTestServer(t)
	s.authCodes.Store("expired-code", &authCodeEntry{
		JWT:       "fake.jwt.token",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"expired-code"}}
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func pkcePair() (verifier, challenge string) {
	verifier = "test-code-verifier-that-is-long-enough-1234567890"
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return
}

func TestOAuthToken_MissingVerifier(t *testing.T) {
	s, r := newRoutedTestServer(t)
	_, challenge := pkcePair()
	s.authCodes.Store("code1", &authCodeEntry{
		JWT: "fake.jwt.token", CodeChallenge: challenge, CodeChallengeMethod: "S256",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"code1"}}
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthToken_WrongVerifier(t *testing.T) {
	s, r := newRoutedTestServer(t)
	_, challenge := pkcePair()
	s.authCodes.Store("code1", &authCodeEntry{
		JWT: "fake.jwt.token", CodeChallenge: challenge, CodeChallengeMethod: "S256",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"code1"}, "code_verifier": {"wrong-verifier"}}
	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestOAuthToken_CorrectVerifierSucceedsAndIsSingleUse(t *testing.T) {
	s, r := newRoutedTestServer(t)
	verifier, challenge := pkcePair()
	s.authCodes.Store("code1", &authCodeEntry{
		JWT: "fake.jwt.token", CodeChallenge: challenge, CodeChallengeMethod: "S256",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"code1"}, "code_verifier": {verifier}}

	req := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "fake.jwt.token", resp["access_token"])
	assert.Equal(t, "Bearer", resp["token_type"])

	// Second call with the same (now-consumed) code must fail.
	req2 := httptest.NewRequest("POST", "/mcp/token", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, 400, w2.Code)
}

// --- sweep (cleanupLoop logic, driven synchronously) ---

func TestSweep_RemovesExpiredCodesAndStates(t *testing.T) {
	s, _ := newRoutedTestServer(t)
	now := time.Now()

	s.authCodes.Store("expired", &authCodeEntry{ExpiresAt: now.Add(-time.Second)})
	s.authCodes.Store("fresh", &authCodeEntry{ExpiresAt: now.Add(time.Minute)})
	s.authStates.Store("expired-state", &mcpAuthState{CreatedAt: now.Add(-11 * time.Minute)})
	s.authStates.Store("fresh-state", &mcpAuthState{CreatedAt: now})

	s.sweep(now)

	_, ok := s.authCodes.Load("expired")
	assert.False(t, ok)
	_, ok = s.authCodes.Load("fresh")
	assert.True(t, ok)
	_, ok = s.authStates.Load("expired-state")
	assert.False(t, ok)
	_, ok = s.authStates.Load("fresh-state")
	assert.True(t, ok)
}

// --- requireAuth / session tracking ---

func TestRequireAuth_MissingAndInvalidToken(t *testing.T) {
	_, r := newRoutedTestServer(t)

	req := httptest.NewRequest("GET", "/mcp/sse", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)
	assert.NotEmpty(t, w.Header().Get("WWW-Authenticate"))

	req = httptest.NewRequest("GET", "/mcp/sse", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)
}

func TestConnectedClients_FiltersByIdentity(t *testing.T) {
	s, _ := newRoutedTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mcpSessions.Store("sess1", &mcpClientSession{ID: "sess1", Identity: "alice@example.com", ctx: ctx})
	s.mcpSessions.Store("sess2", &mcpClientSession{ID: "sess2", Identity: "bob@example.com", ctx: ctx})

	alice := s.ConnectedClients("alice@example.com")
	require.Len(t, alice, 1)
	assert.Equal(t, "sess1", alice[0].ID)

	all := s.ConnectedClients("")
	assert.Len(t, all, 2)
}

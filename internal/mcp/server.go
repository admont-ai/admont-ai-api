package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/christianfischer/md-wiki-server/internal/draft"
	"github.com/christianfischer/md-wiki-server/internal/permissions"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/indexer"
	"github.com/christianfischer/md-wiki-server/internal/repo"
	"github.com/christianfischer/md-wiki-server/internal/repoactions"
	"github.com/christianfischer/md-wiki-server/internal/store/git_repo"
	"github.com/gin-gonic/gin"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
)

type contextKey string

const (
	userEmailKey    contextKey = "mcp_user_email"
	userNameKey     contextKey = "mcp_user_name"
	userIdentityKey contextKey = "mcp_user_identity"
)

func getUserEmail(ctx context.Context) string {
	v, _ := ctx.Value(userEmailKey).(string)
	return v
}

func getUserName(ctx context.Context) string {
	v, _ := ctx.Value(userNameKey).(string)
	return v
}

func getUserIdentity(ctx context.Context) string {
	v, _ := ctx.Value(userIdentityKey).(string)
	return v
}

// Server wraps the MCP protocol server and integrates with the wiki backend.
type Server struct {
	mcpServer                *mcpserver.MCPServer
	sseServer                *mcpserver.SSEServer
	streamableServer         *mcpserver.StreamableHTTPServer
	protectedResourceHandler http.Handler

	backends      map[string]repo.RepoBackend
	draftManagers map[string]*draft.Manager
	docPaths      map[string]string
	repoConfigs   map[string]*git_repo.GitRepo
	repoReady     *sync.Map
	permResolvers map[string]*permissions.Resolver
	isSystemAdmin func(string) bool
	idx           *indexer.Indexer
	searchBackend *backend.Holder
	repoState     backend.RepoStateStore

	jwtService *auth.JWTService
	registry   *auth.Registry
	authn      *auth.Authenticator
	baseURL    string

	// MCP OAuth: authorization codes and pending auth states
	authCodes  sync.Map // code string → *authCodeEntry
	authStates sync.Map // provider state string → *mcpAuthState

	// Registered MCP OAuth clients (client_id → *mcpRegisteredClient)
	registeredClients sync.Map

	// Connected MCP sessions
	mcpSessions sync.Map // sessionID string → *mcpClientSession
}

type mcpRegisteredClient struct {
	RedirectURIs []string
	CreatedAt    time.Time
}

// mcpClientSession tracks an active MCP client connection.
type mcpClientSession struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Identity    string    `json:"identity"`
	ConnectedAt time.Time `json:"connected_at"`
	ctx         context.Context
}

type mcpAuthState struct {
	ClientRedirectURI   string
	ClientState         string
	CodeChallenge       string
	CodeChallengeMethod string
	ProviderName        string
	CreatedAt           time.Time
}

type authCodeEntry struct {
	JWT                 string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

// NewServer creates a new MCP server wired to the wiki backend.
func NewServer(
	backends map[string]repo.RepoBackend,
	draftManagers map[string]*draft.Manager,
	docPaths map[string]string,
	repoConfigs map[string]*git_repo.GitRepo,
	repoReady *sync.Map,
	permResolvers map[string]*permissions.Resolver,
	jwtService *auth.JWTService,
	registry *auth.Registry,
	baseURL string,
) *Server {
	s := &Server{
		backends:      backends,
		draftManagers: draftManagers,
		docPaths:      docPaths,
		repoConfigs:   repoConfigs,
		repoReady:     repoReady,
		permResolvers: permResolvers,
		jwtService:    jwtService,
		registry:      registry,
		baseURL:       strings.TrimRight(baseURL, "/"),
	}

	s.mcpServer = mcpserver.NewMCPServer(
		"md-wiki-server",
		"1.0.0",
		mcpserver.WithToolCapabilities(true),
	)

	s.registerTools()

	s.sseServer = mcpserver.NewSSEServer(s.mcpServer,
		mcpserver.WithBaseURL(s.baseURL),
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEEndpoint("/sse"),
		mcpserver.WithMessageEndpoint("/message"),
		mcpserver.WithSSEContextFunc(s.contextFromRequest),
	)

	// Streamable HTTP transport (mounted at /mcp), alongside the SSE
	// transport above for backward compatibility with existing clients.
	// Stateless: SSE session tracking already lives outside the library in
	// s.mcpSessions, so a second, library-managed session store would just
	// duplicate that without persisting anything either (persistence is
	// explicitly out of scope).
	s.streamableServer = mcpserver.NewStreamableHTTPServer(s.mcpServer,
		mcpserver.WithEndpointPath("/mcp"),
		mcpserver.WithHTTPContextFunc(s.contextFromRequest),
		mcpserver.WithStateLess(true),
	)

	s.protectedResourceHandler = mcpserver.NewProtectedResourceMetadataHandler(mcpserver.ProtectedResourceMetadataConfig{
		Resource:               s.baseURL,
		AuthorizationServers:   []string{s.baseURL},
		BearerMethodsSupported: []string{"header"},
	})

	go s.cleanupLoop()

	return s
}

func (s *Server) SetSystemAdminCheck(fn func(string) bool) {
	s.isSystemAdmin = fn
}

// SetAuthenticator enables native internal-user login (password + TOTP) for the
// MCP browser OAuth flow, removing the dependency on an external IdP.
func (s *Server) SetAuthenticator(a *auth.Authenticator) {
	s.authn = a
}

func (s *Server) SetIndexer(idx *indexer.Indexer) {
	s.idx = idx
}

func (s *Server) SetSearch(b *backend.Holder, rs backend.RepoStateStore) {
	s.searchBackend = b
	s.repoState = rs
}

// RegisterRoutes mounts all MCP-related routes on the Gin engine.
func (s *Server) RegisterRoutes(r *gin.Engine) {
	// OAuth discovery metadata
	r.GET("/.well-known/oauth-authorization-server", s.oauthMetadata)
	r.GET("/.well-known/oauth-protected-resource", gin.WrapH(s.protectedResourceHandler))

	mcp := r.Group("/mcp")
	// OAuth endpoints
	mcp.POST("/register", s.oauthRegister)
	mcp.GET("/authorize", s.oauthAuthorize)
	mcp.POST("/login", s.mcpLogin)
	mcp.GET("/callback", s.oauthCallback)
	mcp.POST("/token", s.oauthToken)
	// SSE transport (auth required — returns 401 to trigger MCP OAuth flow)
	mcp.GET("/sse", s.requireAuth, gin.WrapF(s.sseServer.ServeHTTP))
	mcp.POST("/message", s.requireAuthMessage, gin.WrapF(s.sseServer.ServeHTTP))
	// Streamable HTTP transport (GET/POST/DELETE all on the same path).
	mcp.Any("/", s.requireAuth, gin.WrapF(s.streamableServer.ServeHTTP))
	mcp.Any("", s.requireAuth, gin.WrapF(s.streamableServer.ServeHTTP))
}

// bearerToken extracts the JWT from the Authorization header or access_token query param.
func bearerToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return c.Query("access_token")
}

// requireAuthMessage validates the JWT on a request without creating a session.
// Used for the /mcp/message endpoint, which must not be reachable unauthenticated.
func (s *Server) requireAuthMessage(c *gin.Context) {
	token := bearerToken(c)
	if token == "" {
		s.sendUnauthorized(c)
		return
	}
	if _, err := s.jwtService.ValidateToken(token); err != nil {
		log.WithError(err).Warn("MCP: invalid or expired JWT on message")
		s.sendUnauthorized(c)
		return
	}
	c.Next()
}

// requireAuth rejects unauthenticated requests with 401 + WWW-Authenticate header.
func (s *Server) requireAuth(c *gin.Context) {
	token := bearerToken(c)
	if token == "" {
		s.sendUnauthorized(c)
		return
	}
	claims, err := s.jwtService.ValidateToken(token)
	if err != nil {
		log.WithError(err).Warn("MCP: invalid or expired JWT")
		s.sendUnauthorized(c)
		return
	}

	identity := claims.Identity
	if identity == "" {
		identity = claims.Email
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err == nil {
		sessionID := base64.RawURLEncoding.EncodeToString(b)
		s.mcpSessions.Store(sessionID, &mcpClientSession{
			ID:          sessionID,
			Email:       claims.Email,
			Name:        claims.Name,
			Identity:    identity,
			ConnectedAt: time.Now(),
			ctx:         c.Request.Context(),
		})
		go func() {
			<-c.Request.Context().Done()
			s.mcpSessions.Delete(sessionID)
		}()
	}

	c.Next()
}

func (s *Server) sendUnauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", fmt.Sprintf(
		`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`,
		s.baseURL,
	))
	c.AbortWithStatus(http.StatusUnauthorized)
}

// contextFromRequest validates the JWT from the SSE connection and injects user info.
func (s *Server) contextFromRequest(ctx context.Context, r *http.Request) context.Context {
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	if token == "" {
		token = r.URL.Query().Get("access_token")
	}
	if token == "" {
		return ctx
	}

	claims, err := s.jwtService.ValidateToken(token)
	if err != nil {
		log.WithError(err).Warn("MCP: invalid JWT on SSE connection")
		return ctx
	}

	identity := claims.Identity
	if identity == "" {
		identity = claims.Email
	}

	ctx = context.WithValue(ctx, userEmailKey, claims.Email)
	ctx = context.WithValue(ctx, userNameKey, claims.Name)
	ctx = context.WithValue(ctx, userIdentityKey, identity)
	return ctx
}

// ConnectedClients returns active MCP sessions for the given identity.
// If identity is empty, returns all sessions.
func (s *Server) ConnectedClients(identity string) []mcpClientSession {
	var result []mcpClientSession
	s.mcpSessions.Range(func(_, value any) bool {
		sess := value.(*mcpClientSession)
		if sess.ctx.Err() != nil {
			s.mcpSessions.Delete(sess.ID)
			return true
		}
		if identity == "" || sess.Identity == identity || sess.Email == identity {
			result = append(result, *sess)
		}
		return true
	})
	return result
}

// --- OAuth Authorization Server for MCP Clients ---

func (s *Server) oauthMetadata(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                s.baseURL,
		"authorization_endpoint":                s.baseURL + "/mcp/authorize",
		"token_endpoint":                        s.baseURL + "/mcp/token",
		"registration_endpoint":                 s.baseURL + "/mcp/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// oauthRegister implements RFC 7591 Dynamic Client Registration.
func (s *Server) oauthRegister(c *gin.Context) {
	var req map[string]any
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	clientID := base64.RawURLEncoding.EncodeToString(b)

	var redirectURIs []string
	if uris, ok := req["redirect_uris"]; ok {
		if uriList, ok := uris.([]any); ok {
			for _, u := range uriList {
				if s, ok := u.(string); ok {
					redirectURIs = append(redirectURIs, s)
				}
			}
		}
	}

	s.registeredClients.Store(clientID, &mcpRegisteredClient{
		RedirectURIs: redirectURIs,
		CreatedAt:    time.Now(),
	})

	resp := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"token_endpoint_auth_method": "none",
	}
	if len(redirectURIs) > 0 {
		resp["redirect_uris"] = redirectURIs
	}
	if name, ok := req["client_name"]; ok {
		resp["client_name"] = name
	}

	c.JSON(http.StatusCreated, resp)
}

func (s *Server) validateClientRedirectURI(clientID, redirectURI string) bool {
	if clientID == "" {
		return false
	}
	val, ok := s.registeredClients.Load(clientID)
	if !ok {
		return false
	}
	client := val.(*mcpRegisteredClient)
	for _, uri := range client.RedirectURIs {
		if uri == redirectURI {
			return true
		}
	}
	return len(client.RedirectURIs) == 0
}

func (s *Server) oauthAuthorize(c *gin.Context) {
	clientID := c.Query("client_id")
	redirectURI := c.Query("redirect_uri")
	state := c.Query("state")
	codeChallenge := c.Query("code_challenge")
	codeChallengeMethod := c.Query("code_challenge_method")
	provider := c.Query("provider")

	if redirectURI == "" || codeChallenge == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_uri and code_challenge are required"})
		return
	}
	if !s.validateClientRedirectURI(clientID, redirectURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_uri not registered for this client"})
		return
	}
	if codeChallengeMethod == "" {
		codeChallengeMethod = "S256"
	}

	params := mcpAuthParams{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		State:               state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
	}

	// Build the available provider list: native internal login (if enabled)
	// plus any external IdP providers registered in the registry.
	external := s.registry.Names()
	sort.Strings(external)
	var choices []string
	if s.authn != nil {
		choices = append(choices, "internal")
	}
	choices = append(choices, external...)

	if provider == "" {
		switch len(choices) {
		case 0:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no auth providers configured"})
			return
		case 1:
			provider = choices[0]
		default:
			s.renderProviderChooser(c, params, choices)
			return
		}
	}

	// Native internal login (password + TOTP) — no external IdP round-trip.
	if provider == "internal" {
		if s.authn == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal auth not available"})
			return
		}
		renderNativeLoginPage(c, params, "")
		return
	}

	// Start OAuth flow via registry (external IdP)
	providerAuthURL, providerState, err := s.registry.GenerateAuthURL(provider, "")
	if err != nil {
		log.WithError(err).Error("MCP: failed to generate auth URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate authentication"})
		return
	}

	// Track the MCP client's request, keyed by the provider OAuth state
	s.authStates.Store(providerState, &mcpAuthState{
		ClientRedirectURI:   redirectURI,
		ClientState:         state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ProviderName:        provider,
		CreatedAt:           time.Now(),
	})

	c.Redirect(http.StatusTemporaryRedirect, providerAuthURL)
}

// renderProviderChooser shows an HTML page letting the user pick an auth provider.
// It preserves all original query parameters and adds the selected provider.
func (s *Server) renderProviderChooser(c *gin.Context, _ mcpAuthParams, providers []string) {
	// Build buttons that re-submit to the same endpoint with ?provider=name added.
	var buttons strings.Builder
	for _, p := range providers {
		q := c.Request.URL.Query()
		q.Set("provider", p)
		href := "/mcp/authorize?" + q.Encode()
		label := auth.ProviderDisplayName(p)
		if p == "internal" {
			label = "Internal Account"
		}
		fmt.Fprintf(&buttons,
			`<a href="%s" class="btn">%s</a>`,
			href, label,
		)
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Choose Login Provider</title>
<style>
  body{font-family:system-ui,sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;background:#f5f5f5}
  .card{background:#fff;border-radius:12px;padding:2rem;box-shadow:0 2px 8px rgba(0,0,0,.1);text-align:center;max-width:360px;width:100%%}
  h2{margin:0 0 1.5rem;color:#333}
  .btn{display:block;padding:.75rem 1.5rem;margin:.5rem 0;border-radius:8px;background:#2563eb;color:#fff;text-decoration:none;font-size:1rem;transition:background .15s}
  .btn:hover{background:#1d4ed8}
</style></head>
<body><div class="card"><h2>Sign in with</h2>%s</div></body></html>`, buttons.String())))
}

func (s *Server) oauthCallback(c *gin.Context) {
	code := c.Query("code")
	providerState := c.Query("state")

	if code == "" || providerState == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code and state are required"})
		return
	}

	// Retrieve the MCP auth state
	stateVal, ok := s.authStates.LoadAndDelete(providerState)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired state"})
		return
	}
	mcpState := stateVal.(*mcpAuthState)

	if time.Since(mcpState.CreatedAt) > 10*time.Minute {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authentication timed out"})
		return
	}

	// Exchange code and fetch user info via registry
	userInfo, _, err := s.registry.ExchangeAndValidate(c.Request.Context(), providerState, code)
	if err != nil {
		log.WithError(err).Warn("MCP OAuth: code exchange failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	s.issueMCPCode(c, mcpState.ClientRedirectURI, mcpState.ClientState,
		mcpState.CodeChallenge, mcpState.CodeChallengeMethod,
		userInfo.Email, userInfo.Name, userInfo.Subject, userInfo.Provider)
}

// issueMCPCode mints the app JWT for the authenticated user, stores it under a
// fresh single-use authorization code, and redirects the browser back to the
// MCP client's redirect_uri with that code. Shared by the native and external
// (registry) authorization paths.
func (s *Server) issueMCPCode(c *gin.Context, redirectURI, clientState, codeChallenge, codeChallengeMethod, email, name, subject, provider string) {
	jwt, err := s.jwtService.GenerateToken(email, name, subject, provider)
	if err != nil {
		log.WithError(err).Error("MCP OAuth: failed to generate JWT")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	authCode := base64.RawURLEncoding.EncodeToString(codeBytes)

	s.authCodes.Store(authCode, &authCodeEntry{
		JWT:                 jwt,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().Add(5 * time.Minute),
	})

	u, err := url.Parse(redirectURI)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid redirect_uri"})
		return
	}
	q := u.Query()
	q.Set("code", authCode)
	if clientState != "" {
		q.Set("state", clientState)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusTemporaryRedirect, u.String())
}

func (s *Server) oauthToken(c *gin.Context) {
	grantType := c.PostForm("grant_type")
	if grantType != "authorization_code" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported grant_type"})
		return
	}

	code := c.PostForm("code")
	codeVerifier := c.PostForm("code_verifier")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	codeVal, ok := s.authCodes.LoadAndDelete(code)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired code"})
		return
	}
	entry := codeVal.(*authCodeEntry)

	if time.Now().After(entry.ExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code expired"})
		return
	}

	// Verify PKCE — require verifier whenever a challenge was issued.
	if entry.CodeChallengeMethod == "S256" {
		if codeVerifier == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "code_verifier is required"})
			return
		}
		hash := sha256.Sum256([]byte(codeVerifier))
		computed := base64.RawURLEncoding.EncodeToString(hash[:])
		if computed != entry.CodeChallenge {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid code_verifier"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token": entry.JWT,
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

// --- Permission & Access Helpers ---

func (s *Server) isReadOnly(repoSlug string) bool {
	rc, ok := s.repoConfigs[repoSlug]
	return ok && rc.ReadOnly
}

// actionDeps builds the repoactions.Deps for one repo/identity pair.
func (s *Server) actionDeps(repoSlug, identity string) repoactions.Deps {
	return repoactions.Deps{
		Backend:         s.backends[repoSlug],
		RepoSlug:        repoSlug,
		Resolver:        s.permResolvers[repoSlug],
		ReadOnly:        s.isReadOnly(repoSlug),
		IsAdmin:         identity != "" && s.isSystemAdmin != nil && s.isSystemAdmin(identity),
		Indexer:         s.idx,
		ShouldIndex:     s.shouldIndex(repoSlug),
		SavePermissions: func() { s.savePermissions(repoSlug) },
	}
}

func (s *Server) checkPermission(repoSlug, identity, path string, required permissions.Level) error {
	return repoactions.CheckPermission(s.actionDeps(repoSlug, identity), identity, path, required)
}

func (s *Server) canAccessRepo(repoSlug, identity string) bool {
	rc, ok := s.repoConfigs[repoSlug]
	if !ok {
		return false
	}
	if rc.PublicAccess {
		return true
	}
	if identity == "" {
		return false
	}
	if s.isSystemAdmin != nil && s.isSystemAdmin(identity) {
		return true
	}
	resolver := s.permResolvers[repoSlug]
	if resolver == nil {
		// No permissions file on a private repo: not accessible to non-admins
		// (checkPermission already denies reads here too).
		return false
	}
	// Accessible if the user has Viewer on the root OR any path entry (matches
	// repo listing). Per-file checks still gate individual files.
	return resolver.HasAnyAccess(identity, permissions.Viewer)
}

// filterSearchResults keeps only the chunks the user is permitted to view,
// capped at topK. It mirrors the per-file checks used by the read tools: admins
// bypass; dot-paths are dropped; a nil resolver means no file-level permissions
// (the repo-level gate already authorized access); otherwise the user must have
// at least Viewer on the chunk's path.
func (s *Server) filterSearchResults(results []backend.SearchResult, identity string, topK int) []backend.SearchResult {
	admin := identity != "" && s.isSystemAdmin != nil && s.isSystemAdmin(identity)
	filtered := make([]backend.SearchResult, 0, len(results))
	for _, r := range results {
		if !admin {
			if isDotPath(r.FilePath) {
				continue
			}
			if resolver := s.permResolvers[r.RepoSlug]; resolver != nil && !resolver.Check(identity, r.FilePath, permissions.Viewer) {
				continue
			}
		}
		filtered = append(filtered, r)
		if topK > 0 && len(filtered) >= topK {
			break
		}
	}
	return filtered
}

func (s *Server) shouldIndex(repoSlug string) bool {
	if s.idx == nil {
		return false
	}
	rc, ok := s.repoConfigs[repoSlug]
	return ok && rc.SearchProviderID != nil
}

func (s *Server) savePermissions(repoSlug string) {
	resolver := s.permResolvers[repoSlug]
	if resolver == nil {
		return
	}
	backend, ok := s.backends[repoSlug]
	if !ok {
		return
	}
	data, err := permissions.Marshal(resolver)
	if err != nil {
		log.WithError(err).WithField("repo", repoSlug).Warn("MCP: failed to marshal permissions file")
		return
	}
	if err := backend.AddFile("", permissions.PermissionsFileName, data); err != nil {
		log.WithError(err).WithField("repo", repoSlug).Warn("MCP: failed to save permissions file")
	}
}

func isDotPath(filePath string) bool { return repoactions.IsDotPath(filePath) }

func splitPath(filePath string) (subfolder, filename string) { return repoactions.SplitPath(filePath) }

func jsonResult(v any) *mcplib.CallToolResult {
	data, err := json.Marshal(v)
	if err != nil {
		return mcplib.NewToolResultError("failed to serialize result")
	}
	return mcplib.NewToolResultText(string(data))
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.authCodes.Range(func(key, value any) bool {
			if now.After(value.(*authCodeEntry).ExpiresAt) {
				s.authCodes.Delete(key)
			}
			return true
		})
		s.authStates.Range(func(key, value any) bool {
			if now.Sub(value.(*mcpAuthState).CreatedAt) > 10*time.Minute {
				s.authStates.Delete(key)
			}
			return true
		})
	}
}

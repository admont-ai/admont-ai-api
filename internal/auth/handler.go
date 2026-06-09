package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type authCodeStore struct {
	Token        string
	RefreshToken string
	ExpiresAt    time.Time
}

type Handler struct {
	registry       *Registry
	jwt            *JWTService
	allowedOrigins []string
	authn          *Authenticator
	users          *users.Store
	externalMode   string // "manual" | "approval" | "auto"
	onUserChange   func()
	authCodes      sync.Map
}

func NewHandler(registry *Registry, jwt *JWTService, allowedOrigins []string, authn *Authenticator, usersStore *users.Store, externalMode string) *Handler {
	return &Handler{
		registry:       registry,
		jwt:            jwt,
		allowedOrigins: allowedOrigins,
		authn:          authn,
		users:          usersStore,
		externalMode:   externalMode,
	}
}

func (h *Handler) isAllowedRedirect(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, allowed := range h.allowedOrigins {
		ao, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		if strings.ToLower(ao.Scheme+"://"+ao.Host) == origin {
			return true
		}
	}
	return false
}

type loginResponse struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

func (h *Handler) Login(c *gin.Context) {
	provider := c.Query("provider")
	if provider == "" {
		// Default to first registered provider for backward compatibility.
		names := h.registry.Names()
		sort.Strings(names)
		if len(names) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no auth providers configured"})
			return
		}
		provider = names[0]
	}

	redirectURI := c.Query("redirect_uri")
	if redirectURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_uri is required"})
		return
	}
	if !h.isAllowedRedirect(redirectURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_uri origin not allowed"})
		return
	}

	authURL, state, err := h.registry.GenerateAuthURL(provider, redirectURI)
	if err != nil {
		log.WithError(err).Error("failed to generate auth URL")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		AuthURL: authURL,
		State:   state,
	})
}

func (h *Handler) Callback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	if code == "" || state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code and state are required"})
		return
	}

	userInfo, frontendRedirect, err := h.registry.ExchangeAndValidate(c.Request.Context(), state, code)
	if err != nil {
		log.WithError(err).Warn("OAuth code exchange failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	provider := userInfo.Provider

	// Gate the external (social) login per the configured signup mode.
	if ok, reason := h.resolveExternalUser(c.Request.Context(), provider, userInfo.Email, userInfo.Name); !ok {
		log.WithFields(log.Fields{"provider": provider, "email": userInfo.Email, "reason": reason}).
			Info("external login denied")
		h.denyRedirect(c, frontendRedirect, reason)
		return
	}

	token, err := h.jwt.GenerateToken(userInfo.Email, userInfo.Name, userInfo.Subject, provider)
	if err != nil {
		log.WithError(err).Error("failed to generate JWT")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	refreshToken, err := h.jwt.GenerateRefreshToken(userInfo.Email, provider)
	if err != nil {
		log.WithError(err).Error("failed to generate refresh token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	if frontendRedirect != "" && h.isAllowedRedirect(frontendRedirect) {
		u, err := url.Parse(frontendRedirect)
		if err == nil {
			codeBytes := make([]byte, 32)
			if _, err := rand.Read(codeBytes); err == nil {
				code := base64.RawURLEncoding.EncodeToString(codeBytes)
				h.authCodes.Store(code, &authCodeStore{
					Token:        token,
					RefreshToken: refreshToken,
					ExpiresAt:    time.Now().Add(30 * time.Second),
				})
				q := u.Query()
				q.Set("code", code)
				u.RawQuery = q.Encode()
				c.Redirect(http.StatusTemporaryRedirect, u.String())
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"token": token, "refresh_token": refreshToken})
}

// SetUserChangeHook registers a callback invoked after the social-login flow
// creates or updates a user record (used to refresh the admin's user cache).
func (h *Handler) SetUserChangeHook(fn func()) { h.onUserChange = fn }

func (h *Handler) notifyUserChange() {
	if h.onUserChange != nil {
		h.onUserChange()
	}
}

// resolveExternalUser applies the external signup mode to a social login.
// It returns (true, "") to allow the login, or (false, reason) to deny it,
// where reason is one of "pending", "not_authorized", or "suspended".
func (h *Handler) resolveExternalUser(ctx context.Context, provider, email, fullName string) (bool, string) {
	if h.users == nil {
		return true, "" // store not wired (e.g. tests) — fail open to legacy behavior
	}
	first, last := splitName(fullName)

	existing, err := h.users.GetUser(ctx, provider, email)
	if err != nil {
		log.WithError(err).Error("external login: user lookup failed")
		return false, "not_authorized"
	}

	if existing == nil {
		switch h.externalMode {
		case "auto":
			if err := h.users.CreateExternalUser(ctx, provider, email, first, last, "active"); err != nil {
				log.WithError(err).Error("external login: auto-provision failed")
				return false, "not_authorized"
			}
			h.notifyUserChange()
			return true, ""
		case "approval":
			if err := h.users.CreateExternalUser(ctx, provider, email, first, last, "pending"); err != nil {
				log.WithError(err).Error("external login: pending-create failed")
				return false, "not_authorized"
			}
			h.notifyUserChange()
			return false, "pending"
		default: // manual
			return false, "not_authorized"
		}
	}

	// Existing user — apply blocking/approval state, then refresh profile from IdP.
	if existing.Suspended {
		return false, "suspended"
	}
	switch existing.Status {
	case "pending":
		// Self-signup awaiting admin approval — stays denied until approved.
		return false, "pending"
	case "invited":
		// Admin pre-authorized this user; first login activates them.
		if err := h.users.SetUserStatus(ctx, provider, email, "active"); err != nil {
			log.WithError(err).Error("external login: activating invited user failed")
			return false, "not_authorized"
		}
		_ = h.users.UpdateUserProfile(ctx, provider, email, first, last)
		h.notifyUserChange()
		return true, ""
	default: // active
		if existing.FirstName != first || existing.LastName != last {
			if err := h.users.UpdateUserProfile(ctx, provider, email, first, last); err != nil {
				log.WithError(err).Warn("external login: profile refresh failed")
			} else {
				h.notifyUserChange() // keep the admin user cache in sync with the IdP name
			}
		}
		return true, ""
	}
}

// denyRedirect sends the browser back to an allow-listed frontend with an
// ?error=<reason> param, or returns a 403 JSON error if no safe redirect exists.
func (h *Handler) denyRedirect(c *gin.Context, frontendRedirect, reason string) {
	if frontendRedirect != "" && h.isAllowedRedirect(frontendRedirect) {
		if u, err := url.Parse(frontendRedirect); err == nil {
			q := u.Query()
			q.Set("error", reason)
			u.RawQuery = q.Encode()
			c.Redirect(http.StatusTemporaryRedirect, u.String())
			return
		}
	}
	c.JSON(http.StatusForbidden, gin.H{"error": reason})
}

// splitName splits an IdP-provided full name into first and last components.
func splitName(full string) (first, last string) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", ""
	}
	parts := strings.SplitN(full, " ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.TrimSpace(parts[1])
}

// Exchange trades a one-time code for tokens (used after OAuth redirect).
func (h *Handler) Exchange(c *gin.Context) {
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code is required"})
		return
	}

	val, ok := h.authCodes.LoadAndDelete(body.Code)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired code"})
		return
	}
	entry := val.(*authCodeStore)
	if time.Now().After(entry.ExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code expired"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": entry.Token, "refresh_token": entry.RefreshToken})
}

// Refresh exchanges a valid refresh token for a new access token.
func (h *Handler) Refresh(c *gin.Context) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.RefreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refresh_token is required"})
		return
	}

	claims, err := h.jwt.ValidateRefreshToken(body.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	name := claims.Email
	if claims.Identity != "" {
		name = claims.Identity
	}

	token, err := h.jwt.GenerateToken(claims.Email, name, "", claims.Provider)
	if err != nil {
		log.WithError(err).Error("failed to generate JWT from refresh")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// providerInfo is returned by the Providers endpoint.
type providerInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Providers returns the list of available auth providers.
func (h *Handler) Providers(c *gin.Context) {
	names := h.registry.Names()
	sort.Strings(names)
	providers := make([]providerInfo, len(names))
	for i, n := range names {
		providers[i] = providerInfo{Name: n, DisplayName: ProviderDisplayName(n)}
	}
	c.JSON(http.StatusOK, providers)
}

// providerDisplayNames maps internal provider names to human-friendly labels.
var providerDisplayNames = map[string]string{
	"hydra":           "Internal User",
	"apple":           "Apple",
	"auth0":           "Auth0",
	"azureadv2":       "Azure AD",
	"bitbucket":       "Bitbucket",
	"cognito":         "AWS Cognito",
	"discord":         "Discord",
	"facebook":        "Facebook",
	"github":          "GitHub",
	"gitlab":          "GitLab",
	"google":          "Google",
	"linkedin":        "LinkedIn",
	"microsoftonline": "Microsoft",
	"okta":            "Okta",
	"openidConnect":   "OpenID Connect",
	"slack":           "Slack",
}

// ProviderDisplayName returns a human-friendly label for a provider name.
func ProviderDisplayName(name string) string {
	if display, ok := providerDisplayNames[name]; ok {
		return display
	}
	return name
}

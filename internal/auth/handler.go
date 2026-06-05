package auth

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	registry *Registry
	jwt      *JWTService
}

func NewHandler(registry *Registry, jwt *JWTService) *Handler {
	return &Handler{
		registry: registry,
		jwt:      jwt,
	}
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

	// Map "hydra" provider to "internal" so the JWT identity matches internal user identities.
	provider := userInfo.Provider
	if provider == "hydra" {
		provider = "internal"
	}

	token, err := h.jwt.GenerateToken(userInfo.Email, userInfo.Name, userInfo.Subject, provider)
	if err != nil {
		log.WithError(err).Error("failed to generate JWT")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	if frontendRedirect != "" {
		u, err := url.Parse(frontendRedirect)
		if err == nil {
			q := u.Query()
			q.Set("token", token)
			u.RawQuery = q.Encode()
			c.Redirect(http.StatusTemporaryRedirect, u.String())
			return
		}
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

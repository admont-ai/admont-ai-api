package auth

import (
	"testing"

	storeauth "github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedProviders(t *testing.T) {
	providers := SupportedProviders()
	assert.True(t, len(providers) > 0, "should return at least one provider")

	expected := []string{"google", "github", "gitlab", "okta", "auth0", "discord", "slack", "facebook", "apple", "linkedin", "bitbucket", "microsoftonline", "azureadv2", "cognito", "openidConnect"}
	for _, p := range expected {
		assert.Contains(t, providers, p, "should include %s", p)
	}

	seen := make(map[string]bool)
	for _, p := range providers {
		assert.False(t, seen[p], "duplicate provider: %s", p)
		seen[p] = true
	}
}

func TestNewProviderFromConfig_UnsupportedProvider(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "unknown-provider",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported auth provider")
}

func TestNewProviderFromConfig_Google(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "google",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	assert.Equal(t, "google", entry.Name)
	assert.NotNil(t, entry.GothProvider)
	assert.NotNil(t, entry.OAuthConfig)
	assert.Equal(t, "test-client-id", entry.OAuthConfig.ClientID)
	assert.Equal(t, "http://localhost/callback", entry.OAuthConfig.RedirectURL)
	assert.Contains(t, entry.OAuthConfig.Scopes, "openid")
}

func TestNewProviderFromConfig_GitHub(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "github",
		ClientID:     "gh-id",
		ClientSecret: "gh-secret",
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	assert.Equal(t, "github", entry.Name)
	assert.Contains(t, entry.OAuthConfig.Scopes, "user:email")
}

func TestNewProviderFromConfig_CustomScopes(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "google",
		ClientID:     "id",
		ClientSecret: "secret",
		Scopes:       []string{"custom-scope"},
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	assert.Equal(t, []string{"custom-scope"}, entry.OAuthConfig.Scopes)
}

func TestNewProviderFromConfig_OktaRequiresIssuerURL(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "okta",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "issuer_url")
}

func TestNewProviderFromConfig_Auth0RequiresDomain(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "auth0",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "domain")
}

func TestNewProviderFromConfig_CognitoRequiresDomain(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "cognito",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "domain")
}

func TestNewProviderFromConfig_OpenIDConnectRequiresIssuerURL(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "openidConnect",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	_, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "issuer_url")
}

func TestNewProviderFromConfig_MicrosoftOnlineDefaultTenant(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "microsoftonline",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	assert.Contains(t, entry.OAuthConfig.Endpoint.AuthURL, "common")
}

func TestNewProviderFromConfig_MicrosoftOnlineCustomTenant(t *testing.T) {
	cfg := storeauth.AuthProvider{
		Name:         "microsoftonline",
		ClientID:     "id",
		ClientSecret: "secret",
		TenantID:     "my-tenant-id",
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	assert.Contains(t, entry.OAuthConfig.Endpoint.AuthURL, "my-tenant-id")
}

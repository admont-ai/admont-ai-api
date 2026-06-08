package auth

import (
	"fmt"

	storeauth "github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/markbates/goth"
	gothapple "github.com/markbates/goth/providers/apple"
	gothauth0 "github.com/markbates/goth/providers/auth0"
	gothazureadv2 "github.com/markbates/goth/providers/azureadv2"
	gothbitbucket "github.com/markbates/goth/providers/bitbucket"
	gothcognito "github.com/markbates/goth/providers/cognito"
	gothdiscord "github.com/markbates/goth/providers/discord"
	gothfacebook "github.com/markbates/goth/providers/facebook"
	gothgithub "github.com/markbates/goth/providers/github"
	gothgitlab "github.com/markbates/goth/providers/gitlab"
	gothgoogle "github.com/markbates/goth/providers/google"
	gothlinkedin "github.com/markbates/goth/providers/linkedin"
	gothmsonline "github.com/markbates/goth/providers/microsoftonline"
	gothokta "github.com/markbates/goth/providers/okta"
	gothoidc "github.com/markbates/goth/providers/openidConnect"
	gothslack "github.com/markbates/goth/providers/slack"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
	oauthfacebook "golang.org/x/oauth2/facebook"
	oauthgitlab "golang.org/x/oauth2/gitlab"
	oauthgoogle "golang.org/x/oauth2/google"
	oauthlinkedin "golang.org/x/oauth2/linkedin"
	oauthslack "golang.org/x/oauth2/slack"
)

// ProviderEntry wraps a goth provider with its OAuth2 config.
type ProviderEntry struct {
	Name         string
	GothProvider goth.Provider
	OAuthConfig  *oauth2.Config
}

// github OAuth2 endpoint (matches goth's defaults).
var githubEndpoint = oauth2.Endpoint{
	AuthURL:  "https://github.com/login/oauth/authorize",
	TokenURL: "https://github.com/login/oauth/access_token",
}

// SupportedProviders returns the provider types that the server can create and manage.
func SupportedProviders() []string {
	return []string{
		"apple",
		"auth0",
		"azureadv2",
		"bitbucket",
		"cognito",
		"discord",
		"facebook",
		"github",
		"gitlab",
		"google",
		"linkedin",
		"microsoftonline",
		"okta",
		"openidConnect",
		"slack",
	}
}

// NewProviderFromConfig creates a ProviderEntry from an AuthProvider config entry.
func NewProviderFromConfig(cfg storeauth.AuthProvider, callbackURL string) (*ProviderEntry, error) {
	switch cfg.Name {
	case "google":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "email", "profile"}
		}
		gp := gothgoogle.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("google", gp, cfg, callbackURL, scopes, oauthgoogle.Endpoint), nil

	case "github":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"user:email", "read:user"}
		}
		gp := gothgithub.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("github", gp, cfg, callbackURL, scopes, githubEndpoint), nil

	case "microsoftonline":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		gp := gothmsonline.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		tenant := cfg.TenantID
		if tenant == "" {
			tenant = "common"
		}
		ep := oauth2.Endpoint{
			AuthURL:  fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", tenant),
			TokenURL: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant),
		}
		return newEntry("microsoftonline", gp, cfg, callbackURL, scopes, ep), nil

	case "azureadv2":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		tenant := gothazureadv2.TenantType(cfg.TenantID)
		if tenant == "" {
			tenant = gothazureadv2.CommonTenant
		}
		azureScopes := make([]gothazureadv2.ScopeType, len(scopes))
		for i, s := range scopes {
			azureScopes[i] = gothazureadv2.ScopeType(s)
		}
		gp := gothazureadv2.New(cfg.ClientID, cfg.ClientSecret, callbackURL, gothazureadv2.ProviderOptions{
			Tenant: tenant,
			Scopes: azureScopes,
		})
		ep := oauth2.Endpoint{
			AuthURL:  fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", tenant),
			TokenURL: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant),
		}
		return newEntry("azureadv2", gp, cfg, callbackURL, scopes, ep), nil

	case "gitlab":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"read_user", "openid", "email"}
		}
		gp := gothgitlab.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("gitlab", gp, cfg, callbackURL, scopes, oauthgitlab.Endpoint), nil

	case "okta":
		if cfg.IssuerURL == "" {
			return nil, fmt.Errorf("okta provider requires issuer_url (e.g. https://yourorg.okta.com)")
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		gp := gothokta.New(cfg.ClientID, cfg.ClientSecret, cfg.IssuerURL, callbackURL, scopes...)
		ep := oauth2.Endpoint{
			AuthURL:  cfg.IssuerURL + "/v1/authorize",
			TokenURL: cfg.IssuerURL + "/v1/token",
		}
		return newEntry("okta", gp, cfg, callbackURL, scopes, ep), nil

	case "auth0":
		if cfg.Domain == "" {
			return nil, fmt.Errorf("auth0 provider requires domain (e.g. yourapp.auth0.com)")
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		gp := gothauth0.New(cfg.ClientID, cfg.ClientSecret, callbackURL, cfg.Domain, scopes...)
		ep := oauth2.Endpoint{
			AuthURL:  fmt.Sprintf("https://%s/authorize", cfg.Domain),
			TokenURL: fmt.Sprintf("https://%s/oauth/token", cfg.Domain),
		}
		return newEntry("auth0", gp, cfg, callbackURL, scopes, ep), nil

	case "openidConnect":
		if cfg.IssuerURL == "" {
			return nil, fmt.Errorf("openidConnect provider requires issuer_url")
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		discoveryURL := cfg.IssuerURL + "/.well-known/openid-configuration"
		gp, err := gothoidc.New(cfg.ClientID, cfg.ClientSecret, callbackURL, discoveryURL, scopes...)
		if err != nil {
			return nil, fmt.Errorf("openidConnect discovery failed: %w", err)
		}
		ep := oauth2.Endpoint{
			AuthURL:  cfg.IssuerURL + "/authorize",
			TokenURL: cfg.IssuerURL + "/token",
		}
		return newEntry("openidConnect", gp, cfg, callbackURL, scopes, ep), nil

	case "facebook":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"email"}
		}
		gp := gothfacebook.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("facebook", gp, cfg, callbackURL, scopes, oauthfacebook.Endpoint), nil

	case "discord":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"identify", "email"}
		}
		gp := gothdiscord.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("discord", gp, cfg, callbackURL, scopes, endpoints.Discord), nil

	case "apple":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"name", "email"}
		}
		gp := gothapple.New(cfg.ClientID, cfg.ClientSecret, callbackURL, nil, scopes...)
		return newEntry("apple", gp, cfg, callbackURL, scopes, endpoints.Apple), nil

	case "slack":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"users:read", "users:read.email"}
		}
		gp := gothslack.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("slack", gp, cfg, callbackURL, scopes, oauthslack.Endpoint), nil

	case "bitbucket":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"account", "email"}
		}
		gp := gothbitbucket.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("bitbucket", gp, cfg, callbackURL, scopes, endpoints.Bitbucket), nil

	case "linkedin":
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"r_liteprofile", "r_emailaddress"}
		}
		gp := gothlinkedin.New(cfg.ClientID, cfg.ClientSecret, callbackURL, scopes...)
		return newEntry("linkedin", gp, cfg, callbackURL, scopes, oauthlinkedin.Endpoint), nil

	case "cognito":
		if cfg.Domain == "" {
			return nil, fmt.Errorf("cognito provider requires domain (e.g. yourpool.auth.us-east-1.amazoncognito.com)")
		}
		scopes := cfg.Scopes
		if len(scopes) == 0 {
			scopes = []string{"openid", "profile", "email"}
		}
		baseURL := fmt.Sprintf("https://%s", cfg.Domain)
		gp := gothcognito.New(cfg.ClientID, cfg.ClientSecret, baseURL, callbackURL, scopes...)
		return newEntry("cognito", gp, cfg, callbackURL, scopes, endpoints.AWSCognito(cfg.Domain)), nil

	default:
		return nil, fmt.Errorf("unsupported auth provider: %q", cfg.Name)
	}
}

// newEntry builds a ProviderEntry with common fields.
func newEntry(name string, gp goth.Provider, cfg storeauth.AuthProvider, callbackURL string, scopes []string, ep oauth2.Endpoint) *ProviderEntry {
	return &ProviderEntry{
		Name:         name,
		GothProvider: gp,
		OAuthConfig: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  callbackURL,
			Scopes:       scopes,
			Endpoint:     ep,
		},
	}
}

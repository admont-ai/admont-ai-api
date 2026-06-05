package auth_provider

type AuthProvider struct {
	Name         string   `mapstructure:"name" yaml:"name"`
	ClientID     string   `mapstructure:"client_id" yaml:"client_id"`
	ClientSecret string   `mapstructure:"client_secret" yaml:"client_secret"`
	TenantID     string   `mapstructure:"tenant_id" yaml:"tenant_id,omitempty"`
	IssuerURL    string   `mapstructure:"issuer_url" yaml:"issuer_url,omitempty"`
	Domain       string   `mapstructure:"domain" yaml:"domain,omitempty"`
	Scopes       []string `mapstructure:"scopes" yaml:"scopes,omitempty"`
}

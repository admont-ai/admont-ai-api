package llm_provider

type LLMConfig struct {
	Name         string `mapstructure:"name"`
	ProviderType string `mapstructure:"provider_type"`
	APIKey       string `mapstructure:"api_key"`
	BaseURL      string `mapstructure:"base_url"`
	MaxTokens    int64  `mapstructure:"max_tokens"`
	DefaultModel string `mapstructure:"default_model"`
}

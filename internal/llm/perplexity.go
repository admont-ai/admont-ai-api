package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var PerplexityDefaultModel = Model{ID: "sonar-pro", Name: "Sonar Pro"}

type PerplexityProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewPerplexityProvider(apiKey string, maxTokens int64) *PerplexityProvider {
	return &PerplexityProvider{
		client: openai.NewClient(
			option.WithBaseURL("https://api.perplexity.ai"),
			option.WithAPIKey(apiKey),
		),
		maxTokens: maxTokens,
	}
}

func (p *PerplexityProvider) Name() string        { return "perplexity" }
func (p *PerplexityProvider) DefaultModel() Model { return PerplexityDefaultModel }

func (p *PerplexityProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = PerplexityDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "perplexity")
}

func (p *PerplexityProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = PerplexityDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "perplexity")
}

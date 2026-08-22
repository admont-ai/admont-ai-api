package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var PerplexityDefaultModel = Model{ID: "sonar-pro", Name: "Sonar Pro"}

// perplexityModels is a hardcoded list of Perplexity's Sonar model family.
// Perplexity's API has no model-listing endpoint (GET /models returns 404 —
// unlike every other OpenAI-compatible provider this codebase supports), so
// dynamic fetching isn't possible; this list is maintained by hand instead.
var perplexityModels = []Model{
	{ID: "sonar", Name: "Sonar"},
	{ID: "sonar-pro", Name: "Sonar Pro"},
	{ID: "sonar-reasoning", Name: "Sonar Reasoning"},
	{ID: "sonar-reasoning-pro", Name: "Sonar Reasoning Pro"},
	{ID: "sonar-deep-research", Name: "Sonar Deep Research"},
	{ID: "r1-1776", Name: "R1-1776 (offline, uncensored)"},
}

// PerplexityFetcher returns Perplexity's hardcoded model list. See
// perplexityModels for why this can't be fetched dynamically.
type PerplexityFetcher struct{}

func NewPerplexityFetcher() *PerplexityFetcher { return &PerplexityFetcher{} }

func (f *PerplexityFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	return perplexityModels, nil
}

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

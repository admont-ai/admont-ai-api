package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var MetaDefaultModel = Model{ID: "Llama-4-Maverick-17B-128E-Instruct-FP8", Name: "Llama 4 Maverick"}

type MetaProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewMetaProvider(apiKey string, maxTokens int64) *MetaProvider {
	return &MetaProvider{
		client: openai.NewClient(
			option.WithBaseURL("https://api.llama.com/compat/v1"),
			option.WithAPIKey(apiKey),
		),
		maxTokens: maxTokens,
	}
}

func (p *MetaProvider) Name() string        { return "meta" }
func (p *MetaProvider) DefaultModel() Model { return MetaDefaultModel }

func (p *MetaProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = MetaDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "meta")
}

func (p *MetaProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = MetaDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "meta")
}

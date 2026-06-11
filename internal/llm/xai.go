package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var XAIDefaultModel = Model{ID: "grok-3", Name: "Grok 3"}

type XAIProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewXAIProvider(apiKey string, maxTokens int64) *XAIProvider {
	return &XAIProvider{
		client: openai.NewClient(
			option.WithBaseURL("https://api.x.ai/v1"),
			option.WithAPIKey(apiKey),
		),
		maxTokens: maxTokens,
	}
}

func (p *XAIProvider) Name() string        { return "xai" }
func (p *XAIProvider) DefaultModel() Model { return XAIDefaultModel }

func (p *XAIProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = XAIDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "xai")
}

func (p *XAIProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = XAIDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "xai")
}

package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var DeepSeekDefaultModel = Model{ID: "deepseek-chat", Name: "DeepSeek V3"}

type DeepSeekProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewDeepSeekProvider(apiKey string, maxTokens int64) *DeepSeekProvider {
	return &DeepSeekProvider{
		client: openai.NewClient(
			option.WithBaseURL("https://api.deepseek.com"),
			option.WithAPIKey(apiKey),
		),
		maxTokens: maxTokens,
	}
}

func (p *DeepSeekProvider) Name() string        { return "deepseek" }
func (p *DeepSeekProvider) DefaultModel() Model { return DeepSeekDefaultModel }

func (p *DeepSeekProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = DeepSeekDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "deepseek")
}

func (p *DeepSeekProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = DeepSeekDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "deepseek")
}

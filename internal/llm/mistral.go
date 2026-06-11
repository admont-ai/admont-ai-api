package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var MistralDefaultModel = Model{ID: "mistral-large-latest", Name: "Mistral Large"}

type MistralProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewMistralProvider(apiKey string, maxTokens int64) *MistralProvider {
	return &MistralProvider{
		client: openai.NewClient(
			option.WithBaseURL("https://api.mistral.ai/v1"),
			option.WithAPIKey(apiKey),
		),
		maxTokens: maxTokens,
	}
}

func (p *MistralProvider) Name() string        { return "mistral" }
func (p *MistralProvider) DefaultModel() Model { return MistralDefaultModel }

func (p *MistralProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = MistralDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "mistral")
}

func (p *MistralProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = MistralDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "mistral")
}

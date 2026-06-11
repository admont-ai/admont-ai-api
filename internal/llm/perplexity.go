package llm

import (
	"context"
	"fmt"

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

func (p *PerplexityProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = PerplexityDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "perplexity")
}

func (p *PerplexityProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = PerplexityDefaultModel.ID
	}
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(model),
		MaxCompletionTokens: openai.Int(p.maxTokens),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("perplexity API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in perplexity response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

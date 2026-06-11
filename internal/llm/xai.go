package llm

import (
	"context"
	"fmt"

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

func (p *XAIProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = XAIDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "xai")
}

func (p *XAIProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = XAIDefaultModel.ID
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
		return "", TokenUsage{}, fmt.Errorf("xai API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in xai response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

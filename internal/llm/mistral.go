package llm

import (
	"context"
	"fmt"

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

func (p *MistralProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = MistralDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "mistral")
}

func (p *MistralProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = MistralDefaultModel.ID
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
		return "", TokenUsage{}, fmt.Errorf("mistral API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in mistral response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

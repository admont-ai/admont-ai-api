package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var DeepSeekModels = []Model{
	{ID: "deepseek-chat", Name: "DeepSeek V3"},
	{ID: "deepseek-reasoner", Name: "DeepSeek R1"},
}

var DeepSeekDefaultModel = DeepSeekModels[0] // DeepSeek V3

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
func (p *DeepSeekProvider) Models() []Model     { return DeepSeekModels }
func (p *DeepSeekProvider) DefaultModel() Model { return DeepSeekDefaultModel }

func (p *DeepSeekProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = DeepSeekDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "deepseek")
}

func (p *DeepSeekProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = DeepSeekDefaultModel.ID
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
		return "", TokenUsage{}, fmt.Errorf("deepseek API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in deepseek response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

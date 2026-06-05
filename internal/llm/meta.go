package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var MetaModels = []Model{
	{ID: "Llama-4-Maverick-17B-128E-Instruct-FP8", Name: "Llama 4 Maverick"},
	{ID: "Llama-4-Scout-17B-16E-Instruct", Name: "Llama 4 Scout"},
	{ID: "Llama-3.3-70B-Instruct", Name: "Llama 3.3 70B"},
	{ID: "Llama-3.2-3B-Instruct", Name: "Llama 3.2 3B"},
	{ID: "Llama-3.2-1B-Instruct", Name: "Llama 3.2 1B"},
}

var MetaDefaultModel = MetaModels[0] // Llama 4 Maverick

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
func (p *MetaProvider) Models() []Model     { return MetaModels }
func (p *MetaProvider) DefaultModel() Model { return MetaDefaultModel }

func (p *MetaProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = MetaDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "meta")
}

func (p *MetaProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = MetaDefaultModel.ID
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
		return "", TokenUsage{}, fmt.Errorf("meta API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in meta response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

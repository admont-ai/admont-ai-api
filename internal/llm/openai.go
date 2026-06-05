package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var OpenAIModels = []Model{
	{ID: string(openai.ChatModelGPT4_1), Name: "GPT-4.1"},
	{ID: string(openai.ChatModelGPT4_1Mini), Name: "GPT-4.1 Mini"},
	{ID: string(openai.ChatModelGPT4o), Name: "GPT-4o"},
	{ID: string(openai.ChatModelGPT4oMini), Name: "GPT-4o Mini"},
	{ID: string(openai.ChatModelO3), Name: "o3"},
	{ID: string(openai.ChatModelO3Mini), Name: "o3 Mini"},
	{ID: string(openai.ChatModelO4Mini), Name: "o4 Mini"},
}

var OpenAIDefaultModel = OpenAIModels[0] // GPT-4.1

type OpenAIProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewOpenAIProvider(apiKey string, maxTokens int64) *OpenAIProvider {
	return &OpenAIProvider{
		client:    openai.NewClient(option.WithAPIKey(apiKey)),
		maxTokens: maxTokens,
	}
}

func (p *OpenAIProvider) Name() string        { return "openai" }
func (p *OpenAIProvider) Models() []Model     { return OpenAIModels }
func (p *OpenAIProvider) DefaultModel() Model { return OpenAIDefaultModel }

func (p *OpenAIProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = OpenAIDefaultModel.ID
	}
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.DeveloperMessage(systemPrompt),
	}
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		default:
			msgs = append(msgs, openai.UserMessage(m.Content))
		}
	}
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(model),
		MaxCompletionTokens: openai.Int(p.maxTokens),
		Messages:            msgs,
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("openai API call: %w", err)
	}
	usage := TokenUsage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens}
	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in openai response")
	}
	return resp.Choices[0].Message.Content, usage, nil
}

func (p *OpenAIProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = OpenAIDefaultModel.ID
	}
	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(model),
		MaxCompletionTokens: openai.Int(p.maxTokens),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.DeveloperMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("openai API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in openai response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

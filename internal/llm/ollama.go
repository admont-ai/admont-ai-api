package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var OllamaModels = []Model{
	{ID: "llama3.2:3b", Name: "Llama 3.2 3B"},
	{ID: "llama3.2:1b", Name: "Llama 3.2 1B"},
	{ID: "llama3.1:8b", Name: "Llama 3.1 8B"},
	{ID: "phi3:mini", Name: "Phi-3 Mini"},
	{ID: "mistral:7b", Name: "Mistral 7B"},
	{ID: "gemma2:2b", Name: "Gemma 2 2B"},
}

var OllamaDefaultModel = OllamaModels[0] // llama3.2:3b

type OllamaProvider struct {
	client    openai.Client
	maxTokens int64
}

func NewOllamaProvider(baseURL string, maxTokens int64) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	}
	return &OllamaProvider{
		client: openai.NewClient(
			option.WithBaseURL(baseURL),
			option.WithAPIKey("ollama"), // Ollama doesn't require a real key
		),
		maxTokens: maxTokens,
	}
}

func (p *OllamaProvider) Name() string        { return "ollama" }
func (p *OllamaProvider) Models() []Model     { return OllamaModels }
func (p *OllamaProvider) DefaultModel() Model { return OllamaDefaultModel }

func (p *OllamaProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	if model == "" {
		model = OllamaDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, p.maxTokens, systemPrompt, messages, "ollama")
}

func (p *OllamaProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	if model == "" {
		model = OllamaDefaultModel.ID
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
		return "", TokenUsage{}, fmt.Errorf("ollama API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}

	if len(resp.Choices) == 0 {
		return "", usage, fmt.Errorf("no choices in ollama response")
	}

	return resp.Choices[0].Message.Content, usage, nil
}

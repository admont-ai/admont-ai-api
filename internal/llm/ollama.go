package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var OllamaDefaultModel = Model{ID: "llama3.2:3b", Name: "Llama 3.2 3B"}

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
func (p *OllamaProvider) DefaultModel() Model { return OllamaDefaultModel }

func (p *OllamaProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = OllamaDefaultModel.ID
	}
	return doOpenAICompatChat(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, messages, "ollama")
}

func (p *OllamaProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = OllamaDefaultModel.ID
	}
	return doOpenAICompatDo(ctx, p.client, model, effectiveTokens(maxTokens, p.maxTokens), systemPrompt, userPrompt, "ollama")
}

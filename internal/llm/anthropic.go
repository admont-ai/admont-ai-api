package llm

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicFetcher fetches models dynamically from the Anthropic API.
type AnthropicFetcher struct {
	client anthropic.Client
}

// NewAnthropicFetcher creates a fetcher for the given API key.
func NewAnthropicFetcher(apiKey string) *AnthropicFetcher {
	return &AnthropicFetcher{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
	}
}

func (f *AnthropicFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	pager := f.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var models []Model
	for pager.Next() {
		m := pager.Current()
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		models = append(models, Model{ID: m.ID, Name: name, Created: m.CreatedAt.Unix()})
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("anthropic models list: %w", err)
	}
	return FilterModels("anthropic", models), nil
}

var AnthropicDefaultModel = Model{ID: string(anthropic.ModelClaudeSonnet4_5), Name: "Claude Sonnet 4.5"}

type AnthropicProvider struct {
	client    anthropic.Client
	maxTokens int64
}

func NewAnthropicProvider(apiKey string, maxTokens int64) *AnthropicProvider {
	return &AnthropicProvider{
		client:    anthropic.NewClient(option.WithAPIKey(apiKey)),
		maxTokens: maxTokens,
	}
}

func (p *AnthropicProvider) Name() string        { return "anthropic" }
func (p *AnthropicProvider) DefaultModel() Model { return AnthropicDefaultModel }

func (p *AnthropicProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = AnthropicDefaultModel.ID
	}
	msgs := make([]anthropic.MessageParam, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	msg, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: effectiveTokens(maxTokens, p.maxTokens),
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  msgs,
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("anthropic API call: %w", err)
	}
	usage := TokenUsage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens}
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			return tb.Text, usage, nil
		}
	}
	return "", usage, fmt.Errorf("no text content in anthropic response")
}

func (p *AnthropicProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	if model == "" {
		model = AnthropicDefaultModel.ID
	}
	msg, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: effectiveTokens(maxTokens, p.maxTokens),
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	})
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("anthropic API call: %w", err)
	}

	usage := TokenUsage{
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}

	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			return tb.Text, usage, nil
		}
	}

	return "", usage, fmt.Errorf("no text content in anthropic response")
}

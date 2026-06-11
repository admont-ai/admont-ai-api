package llm

import "context"

// ChatMessage represents a single turn in a multi-turn conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Provider is the interface that all LLM backends must implement.
type Provider interface {
	Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error)
	DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error)
	DefaultModel() Model
	Name() string
}

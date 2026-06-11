package llm

import "context"

// ChatMessage represents a single turn in a multi-turn conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Provider is the interface that all LLM backends must implement.
// maxTokens is the requested output-token limit for the call; 0 (or any value
// above the provider's configured ceiling) means the provider's ceiling.
type Provider interface {
	Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error)
	DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error)
	DefaultModel() Model
	Name() string
}

// effectiveTokens resolves the output-token limit for a request: the requested
// per-action limit if set and below the provider's ceiling, else the ceiling.
func effectiveTokens(requested, ceiling int64) int64 {
	if requested > 0 && requested < ceiling {
		return requested
	}
	return ceiling
}

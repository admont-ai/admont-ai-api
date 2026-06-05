package ai_conversation

import "time"

type Conversation struct {
	ID        string    `json:"id"`
	UserEmail string    `json:"user_email,omitempty"`
	Title     string    `json:"title"`
	Scope     string    `json:"scope"`
	RepoSlug  string    `json:"repo_slug"`
	FilePath  string    `json:"file_path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID             string      `json:"id"`
	ConversationID string      `json:"conversation_id"`
	Role           string      `json:"role"`
	Content        string      `json:"content"`
	Sources        []Source    `json:"sources,omitempty"`
	TokenUsage     *TokenUsage `json:"token_usage,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
}

type Source struct {
	Repo     string  `json:"repo"`
	FilePath string  `json:"file_path"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

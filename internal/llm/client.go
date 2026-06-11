package llm

import (
	"context"
	"fmt"
	"sync"
)

// Model represents an LLM model.
type Model struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	// Created is the unix timestamp the provider reports for the model's
	// release, or 0 if the provider API does not expose one.
	Created int64 `json:"created,omitempty"`
}

// TokenUsage tracks token consumption for a request.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Client wraps multiple Providers and routes requests by model.
type Client struct {
	mu           sync.RWMutex
	providers    map[string]Provider // keyed by provider type
	registry     *ModelRegistry
	actionLimits map[string]int64 // per-action output-token limits (0/absent = provider ceiling)
}

// NewClient creates a Client backed by the given providers.
func NewClient(registry *ModelRegistry) *Client {
	return &Client{
		providers: make(map[string]Provider),
		registry:  registry,
	}
}

// AddProvider registers a provider under the given type name.
func (c *Client) AddProvider(providerType string, p Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers[providerType] = p
}

// Provider returns the provider for a given type, or nil.
func (c *Client) Provider(providerType string) Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.providers[providerType]
}

// SetActionLimits stores the per-action output-token limits (e.g. "ask",
// "generate", "summarize", "edit"). A zero or missing value means the
// provider's configured max tokens apply.
func (c *Client) SetActionLimits(limits map[string]int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actionLimits = limits
}

// LimitFor returns the configured output-token limit for an action, or 0 when
// unset (provider ceiling applies).
func (c *Client) LimitFor(action string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.actionLimits[action]
}

// Do routes the request to the appropriate provider based on the model's
// provider type. maxTokens is the per-action output limit (0 = provider ceiling).
func (c *Client) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	provType := c.registry.ProviderForModel(model)
	if provType == "" {
		// Fall back to any available provider
		for _, p := range c.providers {
			return p.Do(ctx, model, systemPrompt, userPrompt, maxTokens)
		}
		return "", TokenUsage{}, fmt.Errorf("no LLM providers configured")
	}

	p, ok := c.providers[provType]
	if !ok {
		return "", TokenUsage{}, fmt.Errorf("provider %q not configured", provType)
	}
	return p.Do(ctx, model, systemPrompt, userPrompt, maxTokens)
}

// DoChat routes a multi-turn request to the appropriate provider.
// maxTokens is the per-action output limit (0 = provider ceiling).
func (c *Client) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	provType := c.registry.ProviderForModel(model)
	if provType == "" {
		for _, p := range c.providers {
			return p.DoChat(ctx, model, systemPrompt, messages, maxTokens)
		}
		return "", TokenUsage{}, fmt.Errorf("no LLM providers configured")
	}

	p, ok := c.providers[provType]
	if !ok {
		return "", TokenUsage{}, fmt.Errorf("provider %q not configured", provType)
	}
	return p.DoChat(ctx, model, systemPrompt, messages, maxTokens)
}

// AllModels returns the models available from all known providers.
func (c *Client) AllModels() []Model {
	if c.registry != nil {
		return c.registry.AllModels()
	}
	return nil
}

// ValidModel returns true if the given model ID is known across any provider.
func (c *Client) ValidModel(id string) bool {
	if c.registry != nil {
		return c.registry.ValidModel(id)
	}
	return false
}

// HasProviders returns true if at least one provider is registered.
func (c *Client) HasProviders() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.providers) > 0
}

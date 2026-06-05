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
}

// TokenUsage tracks token consumption for a request.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Client wraps multiple Providers and routes requests by model.
type Client struct {
	mu        sync.RWMutex
	providers map[string]Provider // keyed by provider type
	registry  *ModelRegistry
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

// Do routes the request to the appropriate provider based on the model's provider type.
func (c *Client) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	provType := c.registry.ProviderForModel(model)
	if provType == "" {
		// Fall back to any available provider
		for _, p := range c.providers {
			return p.Do(ctx, model, systemPrompt, userPrompt)
		}
		return "", TokenUsage{}, fmt.Errorf("no LLM providers configured")
	}

	p, ok := c.providers[provType]
	if !ok {
		return "", TokenUsage{}, fmt.Errorf("provider %q not configured", provType)
	}
	return p.Do(ctx, model, systemPrompt, userPrompt)
}

// DoChat routes a multi-turn request to the appropriate provider.
func (c *Client) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	provType := c.registry.ProviderForModel(model)
	if provType == "" {
		for _, p := range c.providers {
			return p.DoChat(ctx, model, systemPrompt, messages)
		}
		return "", TokenUsage{}, fmt.Errorf("no LLM providers configured")
	}

	p, ok := c.providers[provType]
	if !ok {
		return "", TokenUsage{}, fmt.Errorf("provider %q not configured", provType)
	}
	return p.DoChat(ctx, model, systemPrompt, messages)
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

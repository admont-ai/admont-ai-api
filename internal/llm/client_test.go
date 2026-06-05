package llm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	name         string
	models       []Model
	defaultModel Model
	doResult     string
	doUsage      TokenUsage
	doErr        error
}

func (p *mockProvider) Name() string        { return p.name }
func (p *mockProvider) Models() []Model     { return p.models }
func (p *mockProvider) DefaultModel() Model { return p.defaultModel }
func (p *mockProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string) (string, TokenUsage, error) {
	return p.doResult, p.doUsage, p.doErr
}
func (p *mockProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage) (string, TokenUsage, error) {
	return p.doResult, p.doUsage, p.doErr
}

func TestClient_AddProvider(t *testing.T) {
	reg := NewModelRegistry()
	c := NewClient(reg)

	assert.False(t, c.HasProviders())

	p := &mockProvider{name: "test"}
	c.AddProvider("test", p)

	assert.True(t, c.HasProviders())
	assert.Equal(t, p, c.Provider("test"))
	assert.Nil(t, c.Provider("missing"))
}

func TestClient_AllModels(t *testing.T) {
	reg := NewModelRegistry()
	reg.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})
	reg.MarkConfigured("openai")

	c := NewClient(reg)
	models := c.AllModels()
	assert.Equal(t, 1, len(models))
	assert.Equal(t, "gpt-4", models[0].ID)
}

func TestClient_ValidModel(t *testing.T) {
	reg := NewModelRegistry()
	reg.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})
	reg.MarkConfigured("openai")

	c := NewClient(reg)
	assert.True(t, c.ValidModel("gpt-4"))
	assert.False(t, c.ValidModel("nonexistent"))
}

func TestClient_Do_RoutesToCorrectProvider(t *testing.T) {
	reg := NewModelRegistry()
	reg.RegisterFallback("test", []Model{
		{ID: "test-model", Provider: "test"},
	}, Model{ID: "test-model", Provider: "test"})

	c := NewClient(reg)
	p := &mockProvider{
		name:     "test",
		doResult: "response text",
		doUsage:  TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	c.AddProvider("test", p)

	result, usage, err := c.Do(context.Background(), "test-model", "system", "user")
	require.NoError(t, err)
	assert.Equal(t, "response text", result)
	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
}

func TestClient_Do_NoProviderForModel(t *testing.T) {
	reg := NewModelRegistry()
	c := NewClient(reg)

	_, _, err := c.Do(context.Background(), "nonexistent", "system", "user")
	assert.Error(t, err)
}

func TestClient_Do_ProviderError(t *testing.T) {
	reg := NewModelRegistry()
	reg.RegisterFallback("test", []Model{
		{ID: "test-model", Provider: "test"},
	}, Model{ID: "test-model", Provider: "test"})

	c := NewClient(reg)
	p := &mockProvider{
		name:  "test",
		doErr: fmt.Errorf("API error"),
	}
	c.AddProvider("test", p)

	_, _, err := c.Do(context.Background(), "test-model", "system", "user")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestClient_DoChat(t *testing.T) {
	reg := NewModelRegistry()
	reg.RegisterFallback("test", []Model{
		{ID: "test-model", Provider: "test"},
	}, Model{ID: "test-model", Provider: "test"})

	c := NewClient(reg)
	p := &mockProvider{
		name:     "test",
		doResult: "chat response",
		doUsage:  TokenUsage{InputTokens: 200, OutputTokens: 100},
	}
	c.AddProvider("test", p)

	messages := []ChatMessage{
		{Role: "user", Content: "hello"},
	}
	result, usage, err := c.DoChat(context.Background(), "test-model", "system", messages)
	require.NoError(t, err)
	assert.Equal(t, "chat response", result)
	assert.Equal(t, int64(200), usage.InputTokens)
}

func TestClient_HasProviders(t *testing.T) {
	reg := NewModelRegistry()
	c := NewClient(reg)

	assert.False(t, c.HasProviders())

	c.AddProvider("test", &mockProvider{name: "test"})
	assert.True(t, c.HasProviders())
}

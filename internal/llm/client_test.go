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
	defaultModel Model
	doResult     string
	doUsage      TokenUsage
	doErr        error
}

func (p *mockProvider) Name() string        { return p.name }
func (p *mockProvider) DefaultModel() Model { return p.defaultModel }
func (p *mockProvider) Do(ctx context.Context, model, systemPrompt, userPrompt string, maxTokens int64) (string, TokenUsage, error) {
	return p.doResult, p.doUsage, p.doErr
}
func (p *mockProvider) DoChat(ctx context.Context, model, systemPrompt string, messages []ChatMessage, maxTokens int64) (string, TokenUsage, error) {
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
	setDynamic(reg, "openai", []Model{{ID: "gpt-4"}})
	reg.MarkConfigured("openai")

	c := NewClient(reg)
	models := c.AllModels()
	assert.Equal(t, 1, len(models))
	assert.Equal(t, "gpt-4", models[0].ID)
}

func TestClient_ValidModel(t *testing.T) {
	reg := NewModelRegistry()
	setDynamic(reg, "openai", []Model{{ID: "gpt-4"}})
	reg.MarkConfigured("openai")

	c := NewClient(reg)
	assert.True(t, c.ValidModel("gpt-4"))
	assert.False(t, c.ValidModel("nonexistent"))
}

func TestClient_Do_RoutesToCorrectProvider(t *testing.T) {
	reg := NewModelRegistry()
	setDynamic(reg, "test", []Model{{ID: "test-model"}})
	reg.MarkConfigured("test")

	c := NewClient(reg)
	p := &mockProvider{
		name:     "test",
		doResult: "response text",
		doUsage:  TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	c.AddProvider("test", p)

	result, usage, err := c.Do(context.Background(), "test-model", "system", "user", 0)
	require.NoError(t, err)
	assert.Equal(t, "response text", result)
	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
}

func TestClient_Do_NoProviderForModel(t *testing.T) {
	reg := NewModelRegistry()
	c := NewClient(reg)

	_, _, err := c.Do(context.Background(), "nonexistent", "system", "user", 0)
	assert.Error(t, err)
}

func TestClient_Do_ProviderError(t *testing.T) {
	reg := NewModelRegistry()
	setDynamic(reg, "test", []Model{{ID: "test-model"}})
	reg.MarkConfigured("test")

	c := NewClient(reg)
	p := &mockProvider{
		name:  "test",
		doErr: fmt.Errorf("API error"),
	}
	c.AddProvider("test", p)

	_, _, err := c.Do(context.Background(), "test-model", "system", "user", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestClient_DoChat(t *testing.T) {
	reg := NewModelRegistry()
	setDynamic(reg, "test", []Model{{ID: "test-model"}})
	reg.MarkConfigured("test")

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
	result, usage, err := c.DoChat(context.Background(), "test-model", "system", messages, 0)
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

func TestEffectiveTokens(t *testing.T) {
	assert.Equal(t, int64(16384), effectiveTokens(0, 16384), "unset request uses ceiling")
	assert.Equal(t, int64(4096), effectiveTokens(4096, 16384), "request below ceiling wins")
	assert.Equal(t, int64(16384), effectiveTokens(32768, 16384), "request above ceiling is capped")
	assert.Equal(t, int64(16384), effectiveTokens(-1, 16384), "negative request uses ceiling")
}

func TestClient_ActionLimits(t *testing.T) {
	c := NewClient(NewModelRegistry())
	assert.Equal(t, int64(0), c.LimitFor("ask"), "unset limits return 0")

	c.SetActionLimits(map[string]int64{"ask": 4096, "generate": 32768})
	assert.Equal(t, int64(4096), c.LimitFor("ask"))
	assert.Equal(t, int64(32768), c.LimitFor("generate"))
	assert.Equal(t, int64(0), c.LimitFor("summarize"))
}

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRegistry_RegisterFallback(t *testing.T) {
	r := NewModelRegistry()
	models := []Model{
		{ID: "gpt-4", Name: "GPT-4", Provider: "openai"},
		{ID: "gpt-3.5-turbo", Name: "GPT-3.5 Turbo", Provider: "openai"},
	}
	defaultModel := Model{ID: "gpt-4", Name: "GPT-4", Provider: "openai"}

	r.RegisterFallback("openai", models, defaultModel)

	got := r.Models("openai")
	assert.Equal(t, 2, len(got))
	assert.Equal(t, "gpt-4", r.DefaultModel("openai").ID)
}

func TestModelRegistry_ModelsUnknownProvider(t *testing.T) {
	r := NewModelRegistry()
	got := r.Models("unknown")
	assert.Empty(t, got)
}

func TestModelRegistry_DefaultModelUnknownProvider(t *testing.T) {
	r := NewModelRegistry()
	got := r.DefaultModel("unknown")
	assert.Equal(t, Model{}, got)
}

func TestModelRegistry_AllModels(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Name: "GPT-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Name: "GPT-4", Provider: "openai"})
	r.RegisterFallback("anthropic", []Model{
		{ID: "claude-3-opus", Name: "Claude 3 Opus", Provider: "anthropic"},
	}, Model{ID: "claude-3-opus", Name: "Claude 3 Opus", Provider: "anthropic"})

	r.MarkConfigured("openai")
	r.MarkConfigured("anthropic")

	all := r.AllModels()
	assert.Equal(t, 2, len(all))

	ids := make([]string, len(all))
	for i, m := range all {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "gpt-4")
	assert.Contains(t, ids, "claude-3-opus")
}

func TestModelRegistry_AllModels_OnlyConfigured(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})
	r.RegisterFallback("anthropic", []Model{
		{ID: "claude-3-opus", Provider: "anthropic"},
	}, Model{ID: "claude-3-opus", Provider: "anthropic"})

	r.MarkConfigured("openai")

	all := r.AllModels()
	ids := make([]string, len(all))
	for i, m := range all {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "gpt-4")
	assert.NotContains(t, ids, "claude-3-opus")
}

func TestModelRegistry_ValidModel(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})
	r.MarkConfigured("openai")

	assert.True(t, r.ValidModel("gpt-4"))
	assert.False(t, r.ValidModel("nonexistent"))
}

func TestModelRegistry_ProviderForModel(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})
	r.RegisterFallback("anthropic", []Model{
		{ID: "claude-3-opus", Provider: "anthropic"},
	}, Model{ID: "claude-3-opus", Provider: "anthropic"})
	r.MarkConfigured("openai")
	r.MarkConfigured("anthropic")

	assert.Equal(t, "openai", r.ProviderForModel("gpt-4"))
	assert.Equal(t, "anthropic", r.ProviderForModel("claude-3-opus"))
	assert.Equal(t, "", r.ProviderForModel("nonexistent"))
}

func TestModelRegistry_MarkUnmarkConfigured(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("openai", []Model{
		{ID: "gpt-4", Provider: "openai"},
	}, Model{ID: "gpt-4", Provider: "openai"})

	r.MarkConfigured("openai")
	all := r.AllModels()
	assert.Equal(t, 1, len(all))

	r.UnmarkConfigured("openai")
	all = r.AllModels()
	assert.Equal(t, 0, len(all))
}

func TestModelRegistry_DynamicModelsFetcher(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("test", []Model{
		{ID: "fallback-model", Provider: "test"},
	}, Model{ID: "fallback-model", Provider: "test"})

	fetcher := &mockFetcher{
		models: []Model{
			{ID: "dynamic-1", Name: "Dynamic 1", Provider: "test"},
			{ID: "dynamic-2", Name: "Dynamic 2", Provider: "test"},
		},
	}
	r.RegisterFetcher("test", fetcher)

	ctx := context.Background()
	r.FetchAll(ctx)

	models := r.Models("test")
	assert.Equal(t, 2, len(models))
	assert.Equal(t, "dynamic-1", models[0].ID)
}

func TestModelRegistry_FallbackWhenFetcherFails(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFallback("test", []Model{
		{ID: "fallback-model", Provider: "test"},
	}, Model{ID: "fallback-model", Provider: "test"})

	fetcher := &mockFetcher{err: assert.AnError}
	r.RegisterFetcher("test", fetcher)

	ctx := context.Background()
	r.FetchAll(ctx)

	models := r.Models("test")
	assert.Equal(t, 1, len(models))
	assert.Equal(t, "fallback-model", models[0].ID)
}

type mockFetcher struct {
	models []Model
	err    error
}

func (f *mockFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	return f.models, f.err
}

func TestModelRegistry_BuiltInModelLists(t *testing.T) {
	lists := map[string][]Model{
		"anthropic":  AnthropicModels,
		"openai":     OpenAIModels,
		"google":     GoogleModels,
		"deepseek":   DeepSeekModels,
		"meta":       MetaModels,
		"mistral":    MistralModels,
		"perplexity": PerplexityModels,
		"xai":        XAIModels,
	}

	for provider, models := range lists {
		t.Run(provider, func(t *testing.T) {
			require.True(t, len(models) > 0, "%s should have at least one model", provider)

			seen := make(map[string]bool)
			for _, m := range models {
				assert.NotEmpty(t, m.ID, "model ID should not be empty")
				assert.NotEmpty(t, m.Name, "model Name should not be empty")
				assert.False(t, seen[m.ID], "duplicate model ID in %s: %s", provider, m.ID)
				seen[m.ID] = true
			}
		})
	}
}

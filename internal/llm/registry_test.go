package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockFetcher struct {
	models []Model
	err    error
}

func (f *mockFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	return f.models, f.err
}

// setDynamic loads models for a provider through the fetcher path.
func setDynamic(r *ModelRegistry, provider string, models []Model) {
	r.RegisterFetcher(provider, &mockFetcher{models: models})
	r.FetchAll(context.Background())
}

func TestModelRegistry_RegisterDefault(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterDefault("openai", Model{ID: "gpt-4", Name: "GPT-4"})
	assert.Equal(t, "gpt-4", r.DefaultModel("openai").ID)
}

func TestModelRegistry_ModelsUnknownProvider(t *testing.T) {
	r := NewModelRegistry()
	assert.Empty(t, r.Models("unknown"))
}

func TestModelRegistry_DefaultModelUnknownProvider(t *testing.T) {
	r := NewModelRegistry()
	assert.Equal(t, Model{}, r.DefaultModel("unknown"))
}

func TestModelRegistry_AllModels(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{{ID: "gpt-4", Name: "GPT-4"}})
	setDynamic(r, "anthropic", []Model{{ID: "claude-x", Name: "Claude X"}})
	r.MarkConfigured("openai")
	r.MarkConfigured("anthropic")

	all := r.AllModels()
	assert.Equal(t, 2, len(all))

	ids := make([]string, len(all))
	for i, m := range all {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "gpt-4")
	assert.Contains(t, ids, "claude-x")
}

func TestModelRegistry_AllModels_OnlyConfigured(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{{ID: "gpt-4"}})
	setDynamic(r, "anthropic", []Model{{ID: "claude-x"}})
	r.MarkConfigured("openai")

	ids := make([]string, 0)
	for _, m := range r.AllModels() {
		ids = append(ids, m.ID)
	}
	assert.Contains(t, ids, "gpt-4")
	assert.NotContains(t, ids, "claude-x")
}

func TestModelRegistry_AllModels_Favourites(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{
		{ID: "gpt-4", Name: "GPT-4"},
		{ID: "gpt-5", Name: "GPT-5"},
		{ID: "o3", Name: "o3"},
	})
	r.MarkConfigured("openai")
	r.SetFavourites("openai", []string{"o3", "gpt-5"})

	all := r.AllModels()
	assert.Equal(t, 2, len(all))
	// Favourite order is preserved.
	assert.Equal(t, "o3", all[0].ID)
	assert.Equal(t, "gpt-5", all[1].ID)

	// A favourite missing from the fetched list stays selectable.
	r.SetFavourites("openai", []string{"gpt-9"})
	all = r.AllModels()
	assert.Equal(t, 1, len(all))
	assert.Equal(t, "gpt-9", all[0].ID)

	// Clearing favourites restores the full list.
	r.SetFavourites("openai", nil)
	assert.Equal(t, 3, len(r.AllModels()))
}

func TestModelRegistry_ValidModel_IgnoresFavourites(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{{ID: "gpt-4"}, {ID: "o3"}})
	r.MarkConfigured("openai")
	r.SetFavourites("openai", []string{"o3"})

	// gpt-4 is not a favourite but must remain valid for routing.
	assert.True(t, r.ValidModel("gpt-4"))
	assert.True(t, r.ValidModel("o3"))
	assert.False(t, r.ValidModel("nonexistent"))
}

func TestModelRegistry_ProviderForModel(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{{ID: "gpt-4"}})
	setDynamic(r, "anthropic", []Model{{ID: "claude-x"}})
	r.MarkConfigured("openai")
	r.MarkConfigured("anthropic")

	assert.Equal(t, "openai", r.ProviderForModel("gpt-4"))
	assert.Equal(t, "anthropic", r.ProviderForModel("claude-x"))
	assert.Equal(t, "", r.ProviderForModel("nonexistent"))
}

func TestModelRegistry_MarkUnmarkConfigured(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "openai", []Model{{ID: "gpt-4"}})

	r.MarkConfigured("openai")
	r.SetFavourites("openai", []string{"gpt-4"})
	assert.Equal(t, 1, len(r.AllModels()))

	r.UnmarkConfigured("openai")
	assert.Equal(t, 0, len(r.AllModels()))

	// Favourites were cleared along with the configuration.
	r.MarkConfigured("openai")
	assert.Equal(t, 1, len(r.AllModels()))
	assert.Equal(t, "gpt-4", r.AllModels()[0].ID)
}

func TestModelRegistry_FetcherError_KeepsCachedModels(t *testing.T) {
	r := NewModelRegistry()
	setDynamic(r, "test", []Model{{ID: "cached"}})

	r.RegisterFetcher("test", &mockFetcher{err: assert.AnError})
	r.FetchAll(context.Background())

	models := r.Models("test")
	assert.Equal(t, 1, len(models))
	assert.Equal(t, "cached", models[0].ID)
}

func TestModelRegistry_FetchProvider(t *testing.T) {
	r := NewModelRegistry()
	r.RegisterFetcher("test", &mockFetcher{models: []Model{{ID: "fresh"}}})

	models := r.FetchProvider(context.Background(), "test")
	assert.Equal(t, 1, len(models))
	assert.Equal(t, "fresh", models[0].ID)

	// Unknown provider returns whatever is cached (nothing).
	assert.Empty(t, r.FetchProvider(context.Background(), "unknown"))
}

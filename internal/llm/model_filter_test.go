package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func ids(models []Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}

func TestFilterModels_OllamaPassthrough(t *testing.T) {
	models := []Model{
		{ID: "llama3.2:3b"},
		{ID: "nomic-embed-text"}, // would be dropped for any other provider
	}
	got := FilterModels("ollama", models)
	assert.Equal(t, models, got)
}

func TestFilterModels_DropsNonChatModels(t *testing.T) {
	now := time.Now().Unix()
	models := []Model{
		{ID: "gpt-5", Created: now},
		{ID: "text-embedding-3-small", Created: now},
		{ID: "whisper-1", Created: now},
		{ID: "tts-1-hd", Created: now},
		{ID: "dall-e-3", Created: now},
		{ID: "gpt-image-1", Created: now},
		{ID: "omni-moderation-2024-09-26", Created: now},
		{ID: "gpt-4o-realtime-preview", Created: now},
		{ID: "gpt-4o-transcribe", Created: now},
		{ID: "gpt-4o-search-preview", Created: now},
	}
	got := FilterModels("openai", models)
	assert.Equal(t, []string{"gpt-5"}, ids(got))
}

func TestFilterModels_CollapsesSnapshots_PrefersAlias(t *testing.T) {
	now := time.Now().Unix()
	models := []Model{
		{ID: "gpt-4o-2024-08-06", Created: now - 100},
		{ID: "gpt-4o", Created: now},
		{ID: "gpt-4o-2024-11-20", Created: now - 50},
	}
	got := FilterModels("openai", models)
	assert.Equal(t, []string{"gpt-4o"}, ids(got))
}

func TestFilterModels_CollapsesSnapshots_NewestWithoutAlias(t *testing.T) {
	now := time.Now().Unix()
	models := []Model{
		{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4 (old)", Created: now - 1000},
		{ID: "claude-sonnet-4-20251101", Name: "Claude Sonnet 4 (new)", Created: now},
	}
	got := FilterModels("anthropic", models)
	assert.Equal(t, []string{"claude-sonnet-4-20251101"}, ids(got))
}

func TestFilterModels_AgeCutoff(t *testing.T) {
	now := time.Now()
	models := []Model{
		{ID: "gpt-5", Created: now.Unix()},
		{ID: "gpt-3.5-turbo", Created: now.Add(-3 * 365 * 24 * time.Hour).Unix()},
		{ID: "davinci-002", Created: now.Add(-4 * 365 * 24 * time.Hour).Unix()},
		{ID: "undated-model", Created: 0}, // no timestamp → kept
	}
	got := FilterModels("openai", models)
	assert.ElementsMatch(t, []string{"gpt-5", "undated-model"}, ids(got))
}

func TestFilterModels_AllOldKeepsNewest(t *testing.T) {
	old := time.Now().Add(-3 * 365 * 24 * time.Hour)
	models := []Model{
		{ID: "ancient", Created: old.Add(-24 * time.Hour).Unix()},
		{ID: "merely-old", Created: old.Unix()},
	}
	got := FilterModels("openai", models)
	assert.Equal(t, []string{"merely-old"}, ids(got))
}

func TestFilterModels_SortsNewestFirst(t *testing.T) {
	now := time.Now().Unix()
	models := []Model{
		{ID: "older", Created: now - 1000},
		{ID: "newest", Created: now},
		{ID: "middle", Created: now - 500},
	}
	got := FilterModels("openai", models)
	assert.Equal(t, []string{"newest", "middle", "older"}, ids(got))
}

package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAICompatFetcher fetches models from any OpenAI-compatible API.
type OpenAICompatFetcher struct {
	client       openai.Client
	providerType string
}

// NewOpenAICompatFetcher creates a fetcher for the given provider type, API
// key and base URL. If baseURL is empty, the default OpenAI endpoint is used.
func NewOpenAICompatFetcher(providerType, apiKey, baseURL string) *OpenAICompatFetcher {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAICompatFetcher{
		client:       openai.NewClient(opts...),
		providerType: providerType,
	}
}

func (f *OpenAICompatFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	pager := f.client.Models.ListAutoPaging(ctx)
	var models []Model
	for pager.Next() {
		m := pager.Current()
		models = append(models, Model{ID: m.ID, Name: m.ID, Created: m.Created})
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}
	return FilterModels(f.providerType, models), nil
}

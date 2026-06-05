package llm

import (
	"context"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAICompatFetcher fetches models from any OpenAI-compatible API.
type OpenAICompatFetcher struct {
	client openai.Client
}

// NewOpenAICompatFetcher creates a fetcher for the given API key and base URL.
// If baseURL is empty, the default OpenAI endpoint is used.
func NewOpenAICompatFetcher(apiKey, baseURL string) *OpenAICompatFetcher {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAICompatFetcher{
		client: openai.NewClient(opts...),
	}
}

func (f *OpenAICompatFetcher) FetchModels(ctx context.Context) ([]Model, error) {
	pager := f.client.Models.ListAutoPaging(ctx)
	var models []Model
	for pager.Next() {
		m := pager.Current()
		id := m.ID
		if strings.Contains(strings.ToLower(id), "embed") {
			continue
		}
		models = append(models, Model{ID: id, Name: id})
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}
	return models, nil
}

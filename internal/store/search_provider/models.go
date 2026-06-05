package search_provider

import "time"

// SearchProviderConfig represents a configured search backend.
type SearchProviderConfig struct {
	Name         string            `json:"name"`
	ProviderType string            `json:"provider_type"`
	Config       map[string]string `json:"config"`
}

// SearchRepoState tracks the last indexed commit for a repository (in the main DB).
type SearchRepoState struct {
	RepoSlug       string    `json:"repo_slug"`
	LastIndexedSHA string    `json:"last_indexed_sha"`
	TotalChunks    int       `json:"total_chunks"`
	LastIndexedAt  time.Time `json:"last_indexed_at"`
}

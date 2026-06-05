package backend

import (
	"context"
	"time"
)

// Chunk represents a text chunk to be indexed. Backend-agnostic — no embedding vectors.
type Chunk struct {
	RepoSlug    string
	FilePath    string
	ChunkIndex  int
	HeadingPath string
	Content     string
}

// SearchResult represents a single search hit.
type SearchResult struct {
	RepoSlug string  `json:"repo"`
	FilePath string  `json:"file_path"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

// RepoState tracks the last indexed commit for a repository.
type RepoState struct {
	RepoSlug       string    `json:"repo_slug"`
	LastIndexedSHA string    `json:"last_indexed_sha"`
	TotalChunks    int       `json:"total_chunks"`
	LastIndexedAt  time.Time `json:"last_indexed_at"`
}

// SearchBackend defines the contract for pluggable search backends.
// All methods accept text queries — embedding is handled internally by each backend.
type SearchBackend interface {
	// Name returns the backend identifier (e.g. "pgvector", "elasticsearch").
	Name() string

	// Initialize performs any setup needed (create indices, collections, etc.).
	Initialize(ctx context.Context) error

	// Close releases resources held by the backend.
	Close() error

	// UpsertChunks indexes or updates a batch of text chunks.
	UpsertChunks(ctx context.Context, chunks []Chunk) error

	// DeleteFileChunks removes all chunks for a specific file.
	DeleteFileChunks(ctx context.Context, repoSlug, filePath string) error

	// DeleteRepoChunks removes all chunks for an entire repository.
	DeleteRepoChunks(ctx context.Context, repoSlug string) error

	// FulltextSearch performs keyword-based search.
	FulltextSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error)

	// SemanticSearch performs vector similarity search.
	SemanticSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error)

	// HybridSearch combines fulltext and semantic search (e.g. via RRF).
	HybridSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]SearchResult, error)
}

// RepoStateStore provides CRUD for repo indexing state, stored in the main application DB.
type RepoStateStore interface {
	GetSearchRepoState(ctx context.Context, repoSlug string) (*RepoState, error)
	GetAllSearchRepoStates(ctx context.Context) ([]RepoState, error)
	UpdateSearchRepoState(ctx context.Context, repoSlug, sha string) error
	DeleteSearchRepoState(ctx context.Context, repoSlug string) error
	ClearAllSearchRepoStates(ctx context.Context) error
}

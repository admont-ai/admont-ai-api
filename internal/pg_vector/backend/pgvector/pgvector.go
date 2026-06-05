package pgvector

import (
	"context"

	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/christianfischer/md-wiki-server/internal/pg_vector/embedder"
	searchstore "github.com/christianfischer/md-wiki-server/internal/pg_vector/store"
)

// Backend wraps the existing pgvector search store and local ONNX embedder.
type Backend struct {
	store *searchstore.Store
	emb   *embedder.Embedder
}

// New creates a pgvector backend. It connects to the search DB and runs migrations.
func New(ctx context.Context, searchDSN string, emb *embedder.Embedder) (*Backend, error) {
	if err := searchstore.EnsureDatabase(ctx, searchDSN); err != nil {
		return nil, err
	}
	st, err := searchstore.New(ctx, searchDSN)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return &Backend{store: st, emb: emb}, nil
}

func (b *Backend) Name() string { return "pgvector" }

func (b *Backend) Initialize(_ context.Context) error { return nil }

func (b *Backend) Close() error {
	b.store.Close()
	return nil
}

func (b *Backend) UpsertChunks(ctx context.Context, chunks []backend.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	// Embed all chunk contents via local ONNX
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	var storeChunks []searchstore.Chunk
	batchSize := 32
	idx := 0
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		embeddings, err := b.emb.EmbedBatch(texts[i:end])
		if err != nil {
			return err
		}
		for j, emb := range embeddings {
			c := chunks[i+j]
			storeChunks = append(storeChunks, searchstore.Chunk{
				RepoSlug:    c.RepoSlug,
				FilePath:    c.FilePath,
				ChunkIndex:  c.ChunkIndex,
				HeadingPath: c.HeadingPath,
				Content:     c.Content,
				Embedding:   emb,
			})
			idx++
		}
	}

	return b.store.UpsertChunks(ctx, storeChunks)
}

func (b *Backend) DeleteFileChunks(ctx context.Context, repoSlug, filePath string) error {
	return b.store.DeleteFileChunks(ctx, repoSlug, filePath)
}

func (b *Backend) DeleteRepoChunks(ctx context.Context, repoSlug string) error {
	return b.store.DeleteRepoChunks(ctx, repoSlug)
}

func (b *Backend) FulltextSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]backend.SearchResult, error) {
	results, err := b.store.FulltextSearch(ctx, repos, query, pathPrefix, topK, threshold)
	if err != nil {
		return nil, err
	}
	return convertResults(results), nil
}

func (b *Backend) SemanticSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]backend.SearchResult, error) {
	embedding, err := b.emb.Embed(query)
	if err != nil {
		return nil, err
	}
	results, err := b.store.SemanticSearch(ctx, repos, embedding, pathPrefix, topK, threshold)
	if err != nil {
		return nil, err
	}
	return convertResults(results), nil
}

func (b *Backend) HybridSearch(ctx context.Context, repos []string, query, pathPrefix string, topK int, threshold float64) ([]backend.SearchResult, error) {
	embedding, err := b.emb.Embed(query)
	if err != nil {
		return nil, err
	}
	results, err := b.store.HybridSearch(ctx, repos, query, embedding, pathPrefix, topK, threshold)
	if err != nil {
		return nil, err
	}
	return convertResults(results), nil
}

func convertResults(storeResults []searchstore.SearchResult) []backend.SearchResult {
	out := make([]backend.SearchResult, len(storeResults))
	for i, r := range storeResults {
		out[i] = backend.SearchResult{
			RepoSlug: r.RepoSlug,
			FilePath: r.FilePath,
			Chunk:    r.Chunk,
			Score:    r.Score,
		}
	}
	return out
}

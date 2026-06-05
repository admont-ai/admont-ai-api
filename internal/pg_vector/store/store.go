package store

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type Chunk struct {
	RepoSlug    string
	FilePath    string
	ChunkIndex  int
	HeadingPath string
	Content     string
	Embedding   pgvector.Vector
}

type SearchResult struct {
	RepoSlug string  `json:"repo"`
	FilePath string  `json:"file_path"`
	Chunk    string  `json:"chunk"`
	Score    float64 `json:"score"`
}

type RepoState struct {
	RepoSlug       string    `json:"repo_slug"`
	LastIndexedSHA string    `json:"last_indexed_sha"`
	TotalChunks    int       `json:"total_chunks"`
	LastIndexedAt  time.Time `json:"last_indexed_at"`
}

type Store struct {
	pool *pgxpool.Pool
}

// EnsureDatabase creates the target database if it does not already exist.
// It parses the DSN, connects to the default "postgres" database, and issues
// a CREATE DATABASE if needed.
func EnsureDatabase(ctx context.Context, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parsing DSN: %w", err)
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return nil // no database name in DSN, nothing to do
	}

	// Build a DSN pointing at the default "postgres" database.
	adminURL := *u
	adminURL.Path = "/postgres"
	adminDSN := adminURL.String()

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connecting to postgres database: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	err = conn.QueryRow(ctx, "SELECT true FROM pg_database WHERE datname = $1", dbName).Scan(&exists)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("checking database existence: %w", err)
	}

	if !exists {
		// Database names cannot be parameterised, but we trust the value from our own config.
		_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
		if err != nil {
			return fmt.Errorf("creating database %q: %w", dbName, err)
		}
	}
	return nil
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS vector;

		CREATE TABLE IF NOT EXISTS search_chunks (
			id            BIGSERIAL PRIMARY KEY,
			repo_slug     TEXT NOT NULL,
			file_path     TEXT NOT NULL,
			chunk_index   INT NOT NULL,
			heading_path  TEXT NOT NULL DEFAULT '',
			content       TEXT NOT NULL,
			embedding     vector(384) NOT NULL,
			tsv           tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
			UNIQUE (repo_slug, file_path, chunk_index)
		);

		CREATE INDEX IF NOT EXISTS idx_chunks_repo_file ON search_chunks (repo_slug, file_path);
		CREATE INDEX IF NOT EXISTS idx_chunks_tsv ON search_chunks USING GIN (tsv);
		CREATE INDEX IF NOT EXISTS idx_chunks_embedding ON search_chunks USING hnsw (embedding vector_cosine_ops);

		CREATE TABLE IF NOT EXISTS search_repo_state (
			repo_slug         TEXT PRIMARY KEY,
			last_indexed_sha  TEXT NOT NULL DEFAULT '',
			total_chunks      INT NOT NULL DEFAULT 0,
			last_indexed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	return err
}

func (s *Store) UpsertChunks(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, c := range chunks {
		_, err := tx.Exec(ctx, `
			INSERT INTO search_chunks (repo_slug, file_path, chunk_index, heading_path, content, embedding)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (repo_slug, file_path, chunk_index)
			DO UPDATE SET heading_path = EXCLUDED.heading_path,
			             content = EXCLUDED.content,
			             embedding = EXCLUDED.embedding
		`, c.RepoSlug, c.FilePath, c.ChunkIndex, c.HeadingPath, c.Content, c.Embedding)
		if err != nil {
			return fmt.Errorf("upserting chunk %s/%s#%d: %w", c.RepoSlug, c.FilePath, c.ChunkIndex, err)
		}
	}

	// Delete any chunks beyond the new set for this file
	if len(chunks) > 0 {
		first := chunks[0]
		_, err := tx.Exec(ctx, `
			DELETE FROM search_chunks
			WHERE repo_slug = $1 AND file_path = $2 AND chunk_index >= $3
		`, first.RepoSlug, first.FilePath, len(chunks))
		if err != nil {
			return fmt.Errorf("deleting stale chunks: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeleteFileChunks(ctx context.Context, repoSlug, filePath string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM search_chunks WHERE repo_slug = $1 AND file_path = $2
	`, repoSlug, filePath)
	return err
}

func (s *Store) DeleteRepoChunks(ctx context.Context, repoSlug string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM search_chunks WHERE repo_slug = $1
	`, repoSlug)
	return err
}

func (s *Store) FulltextSearch(ctx context.Context, repos []string, query string, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	sql := `
		SELECT repo_slug, file_path, content,
		       ts_rank_cd(tsv, plainto_tsquery('english', $1)) AS score
		FROM search_chunks
		WHERE tsv @@ plainto_tsquery('english', $1)
		  AND repo_slug = ANY($2)
	`
	args := []any{query, repos}

	if pathPrefix != "" {
		sql += ` AND file_path LIKE $3 || '%'`
		args = append(args, pathPrefix)
	}

	sql += ` ORDER BY score DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, topK)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("fulltext search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var score float64
		if err := rows.Scan(&r.RepoSlug, &r.FilePath, &r.Chunk, &score); err != nil {
			return nil, err
		}
		if score >= threshold {
			r.Score = score
			results = append(results, r)
		}
	}
	return results, rows.Err()
}

func (s *Store) SemanticSearch(ctx context.Context, repos []string, embedding pgvector.Vector, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	sql := `
		SELECT repo_slug, file_path, content,
		       1 - (embedding <=> $1) AS score
		FROM search_chunks
		WHERE repo_slug = ANY($2)
	`
	args := []any{embedding, repos}

	if pathPrefix != "" {
		sql += ` AND file_path LIKE $3 || '%'`
		args = append(args, pathPrefix)
	}

	sql += ` ORDER BY embedding <=> $1 LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, topK)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var score float64
		if err := rows.Scan(&r.RepoSlug, &r.FilePath, &r.Chunk, &score); err != nil {
			return nil, err
		}
		if score >= threshold {
			r.Score = score
			results = append(results, r)
		}
	}
	return results, rows.Err()
}

// HybridSearch runs both fulltext and semantic search with 2*topK, merges with RRF.
func (s *Store) HybridSearch(ctx context.Context, repos []string, query string, embedding pgvector.Vector, pathPrefix string, topK int, threshold float64) ([]SearchResult, error) {
	internalK := topK * 2

	ftResults, err := s.FulltextSearch(ctx, repos, query, pathPrefix, internalK, 0)
	if err != nil {
		return nil, err
	}

	semResults, err := s.SemanticSearch(ctx, repos, embedding, pathPrefix, internalK, 0)
	if err != nil {
		return nil, err
	}

	return rrfMerge(ftResults, semResults, topK, threshold), nil
}

func rrfMerge(ftResults, semResults []SearchResult, topK int, threshold float64) []SearchResult {
	const rrfK = 60.0

	type chunkKey struct {
		repo string
		path string
		text string
	}

	scores := map[chunkKey]float64{}
	items := map[chunkKey]SearchResult{}

	for rank, r := range ftResults {
		ck := chunkKey{r.RepoSlug, r.FilePath, r.Chunk}
		scores[ck] += 1.0 / (rrfK + float64(rank+1))
		items[ck] = r
	}
	for rank, r := range semResults {
		ck := chunkKey{r.RepoSlug, r.FilePath, r.Chunk}
		scores[ck] += 1.0 / (rrfK + float64(rank+1))
		items[ck] = r
	}

	// Collect and sort by RRF score
	type scored struct {
		result SearchResult
		score  float64
	}
	var all []scored
	for ck, s := range scores {
		r := items[ck]
		r.Score = math.Round(s*10000) / 10000
		if r.Score >= threshold {
			all = append(all, scored{r, s})
		}
	}

	// Sort descending by score
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].score > all[i].score {
				all[i], all[j] = all[j], all[i]
			}
		}
	}

	results := make([]SearchResult, 0, topK)
	for i := 0; i < len(all) && i < topK; i++ {
		results = append(results, all[i].result)
	}
	return results
}

func (s *Store) GetRepoState(ctx context.Context, repoSlug string) (*RepoState, error) {
	var state RepoState
	err := s.pool.QueryRow(ctx, `
		SELECT repo_slug, last_indexed_sha, total_chunks, last_indexed_at
		FROM search_repo_state WHERE repo_slug = $1
	`, repoSlug).Scan(&state.RepoSlug, &state.LastIndexedSHA, &state.TotalChunks, &state.LastIndexedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) GetAllRepoStates(ctx context.Context) ([]RepoState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT repo_slug, last_indexed_sha, total_chunks, last_indexed_at
		FROM search_repo_state ORDER BY repo_slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []RepoState
	for rows.Next() {
		var s RepoState
		if err := rows.Scan(&s.RepoSlug, &s.LastIndexedSHA, &s.TotalChunks, &s.LastIndexedAt); err != nil {
			return nil, err
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

func (s *Store) UpdateRepoState(ctx context.Context, repoSlug, sha string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO search_repo_state (repo_slug, last_indexed_sha, total_chunks, last_indexed_at)
		VALUES ($1, $2, (SELECT COUNT(*) FROM search_chunks WHERE repo_slug = $1), NOW())
		ON CONFLICT (repo_slug)
		DO UPDATE SET last_indexed_sha = EXCLUDED.last_indexed_sha,
		             total_chunks = (SELECT COUNT(*) FROM search_chunks WHERE repo_slug = $1),
		             last_indexed_at = NOW()
	`, repoSlug, sha)
	return err
}

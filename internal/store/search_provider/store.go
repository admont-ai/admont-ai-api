package search_provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/christianfischer/md-wiki-server/internal/pg_vector/backend"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool    *pgxpool.Pool
	encrypt func(string) (string, error)
	decrypt func(string) (string, error)
}

func NewStore(pool *pgxpool.Pool, encrypt, decrypt func(string) (string, error)) *Store {
	return &Store{pool: pool, encrypt: encrypt, decrypt: decrypt}
}

// --- Search Providers ---

// ListSearchProviders returns all search providers.
func (s *Store) ListSearchProviders(ctx context.Context) ([]SearchProviderConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, provider_type, config
		FROM search_providers ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing search providers: %w", err)
	}
	defer rows.Close()

	var providers []SearchProviderConfig
	for rows.Next() {
		var p SearchProviderConfig
		var cfgJSON []byte
		if err := rows.Scan(&p.Name, &p.ProviderType, &cfgJSON); err != nil {
			return nil, err
		}
		p.Config = map[string]string{}
		if len(cfgJSON) > 0 {
			_ = json.Unmarshal(cfgJSON, &p.Config)
		}
		s.decryptConfigSecrets(p.Config)
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetSearchProvider retrieves a search provider by name.
func (s *Store) GetSearchProvider(ctx context.Context, name string) (*SearchProviderConfig, error) {
	var p SearchProviderConfig
	var cfgJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT name, provider_type, config
		FROM search_providers WHERE name = $1
	`, name).Scan(&p.Name, &p.ProviderType, &cfgJSON)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting search provider %q: %w", name, err)
	}
	p.Config = map[string]string{}
	if len(cfgJSON) > 0 {
		_ = json.Unmarshal(cfgJSON, &p.Config)
	}
	s.decryptConfigSecrets(p.Config)
	return &p, nil
}

// GetSearchProviderID returns the database ID for a search provider by name.
func (s *Store) GetSearchProviderID(ctx context.Context, name string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `SELECT id FROM search_providers WHERE name = $1`, name).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("search provider %q not found", name)
	}
	if err != nil {
		return 0, fmt.Errorf("getting search provider ID for %q: %w", name, err)
	}
	return id, nil
}

// GetSearchProviderNameByID returns the name for a search provider by ID.
func (s *Store) GetSearchProviderNameByID(ctx context.Context, id int) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM search_providers WHERE id = $1`, id).Scan(&name)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("search provider with id %d not found", id)
	}
	if err != nil {
		return "", fmt.Errorf("getting search provider name for id %d: %w", id, err)
	}
	return name, nil
}

// UpsertSearchProvider inserts or updates a search provider.
func (s *Store) UpsertSearchProvider(ctx context.Context, p SearchProviderConfig) error {
	cfgToStore := make(map[string]string, len(p.Config))
	for k, v := range p.Config {
		cfgToStore[k] = v
	}
	s.encryptConfigSecrets(cfgToStore)
	cfgJSON, err := json.Marshal(cfgToStore)
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO search_providers (name, provider_type, config)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
			provider_type = EXCLUDED.provider_type,
			config = EXCLUDED.config
	`, p.Name, p.ProviderType, cfgJSON)
	if err != nil {
		return fmt.Errorf("upserting search provider %q: %w", p.Name, err)
	}
	return nil
}

// IsSearchProviderInUse returns true if any git repo references this search provider.
func (s *Store) IsSearchProviderInUse(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM git_repos
		WHERE search_provider_id = (SELECT id FROM search_providers WHERE name = $1)
	`, name).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking search provider usage for %q: %w", name, err)
	}
	return count > 0, nil
}

// DeleteSearchProvider removes a search provider by name.
func (s *Store) DeleteSearchProvider(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM search_providers WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting search provider %q: %w", name, err)
	}
	return nil
}

// secretKeys are config keys whose values should be encrypted at rest.
var secretKeys = map[string]bool{
	"api_key":           true,
	"embedding_api_key": true,
	"password":          true,
}

func (s *Store) encryptConfigSecrets(cfg map[string]string) {
	for k, v := range cfg {
		if secretKeys[k] && v != "" {
			if enc, err := s.encrypt(v); err == nil {
				cfg[k] = enc
			}
		}
	}
}

func (s *Store) decryptConfigSecrets(cfg map[string]string) {
	for k, v := range cfg {
		if secretKeys[k] && v != "" {
			if dec, err := s.decrypt(v); err == nil {
				cfg[k] = dec
			}
		}
	}
}

// --- Repo State ---

// GetSearchRepoState returns the indexing state for a single repo.
func (s *Store) GetSearchRepoState(ctx context.Context, repoSlug string) (*SearchRepoState, error) {
	var st SearchRepoState
	err := s.pool.QueryRow(ctx, `
		SELECT repo_slug, last_indexed_sha, total_chunks, last_indexed_at
		FROM search_repo_state WHERE repo_slug = $1
	`, repoSlug).Scan(&st.RepoSlug, &st.LastIndexedSHA, &st.TotalChunks, &st.LastIndexedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting search repo state for %q: %w", repoSlug, err)
	}
	return &st, nil
}

// GetAllSearchRepoStates returns indexing state for all repos.
func (s *Store) GetAllSearchRepoStates(ctx context.Context) ([]SearchRepoState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT repo_slug, last_indexed_sha, total_chunks, last_indexed_at
		FROM search_repo_state ORDER BY repo_slug
	`)
	if err != nil {
		return nil, fmt.Errorf("listing search repo states: %w", err)
	}
	defer rows.Close()

	var states []SearchRepoState
	for rows.Next() {
		var st SearchRepoState
		if err := rows.Scan(&st.RepoSlug, &st.LastIndexedSHA, &st.TotalChunks, &st.LastIndexedAt); err != nil {
			return nil, err
		}
		states = append(states, st)
	}
	return states, rows.Err()
}

// UpdateSearchRepoState upserts the indexing state for a repo.
func (s *Store) UpdateSearchRepoState(ctx context.Context, repoSlug, sha string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO search_repo_state (repo_slug, last_indexed_sha, total_chunks, last_indexed_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (repo_slug)
		DO UPDATE SET last_indexed_sha = EXCLUDED.last_indexed_sha,
		             last_indexed_at = NOW()
	`, repoSlug, sha)
	return err
}

// DeleteSearchRepoState removes the indexing state for a repo.
func (s *Store) DeleteSearchRepoState(ctx context.Context, repoSlug string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM search_repo_state WHERE repo_slug = $1`, repoSlug)
	return err
}

// ClearAllSearchRepoStates removes all repo indexing states (used on backend switch).
func (s *Store) ClearAllSearchRepoStates(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM search_repo_state`)
	return err
}

// --- RepoStateStore adapter ---

// repoStateAdapter wraps *Store to implement backend.RepoStateStore.
type repoStateAdapter struct {
	s *Store
}

// NewRepoStateStore returns a backend.RepoStateStore backed by this search store.
func NewRepoStateStore(s *Store) backend.RepoStateStore {
	return &repoStateAdapter{s: s}
}

func (a *repoStateAdapter) GetSearchRepoState(ctx context.Context, repoSlug string) (*backend.RepoState, error) {
	st, err := a.s.GetSearchRepoState(ctx, repoSlug)
	if err != nil || st == nil {
		return nil, err
	}
	return &backend.RepoState{
		RepoSlug:       st.RepoSlug,
		LastIndexedSHA: st.LastIndexedSHA,
		TotalChunks:    st.TotalChunks,
		LastIndexedAt:  st.LastIndexedAt,
	}, nil
}

func (a *repoStateAdapter) GetAllSearchRepoStates(ctx context.Context) ([]backend.RepoState, error) {
	states, err := a.s.GetAllSearchRepoStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backend.RepoState, len(states))
	for i, st := range states {
		out[i] = backend.RepoState{
			RepoSlug:       st.RepoSlug,
			LastIndexedSHA: st.LastIndexedSHA,
			TotalChunks:    st.TotalChunks,
			LastIndexedAt:  st.LastIndexedAt,
		}
	}
	return out, nil
}

func (a *repoStateAdapter) UpdateSearchRepoState(ctx context.Context, repoSlug, sha string) error {
	return a.s.UpdateSearchRepoState(ctx, repoSlug, sha)
}

func (a *repoStateAdapter) DeleteSearchRepoState(ctx context.Context, repoSlug string) error {
	return a.s.DeleteSearchRepoState(ctx, repoSlug)
}

func (a *repoStateAdapter) ClearAllSearchRepoStates(ctx context.Context) error {
	return a.s.ClearAllSearchRepoStates(ctx)
}

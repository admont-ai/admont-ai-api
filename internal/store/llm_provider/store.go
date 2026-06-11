package llm_provider

import (
	"context"
	"fmt"

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

// ListLLMProviders returns all LLM providers from the database.
func (s *Store) ListLLMProviders(ctx context.Context) ([]LLMConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, provider_type, api_key, base_url, max_tokens, default_model, favourite_models
		FROM llm_providers ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing LLM providers: %w", err)
	}
	defer rows.Close()

	var providers []LLMConfig
	for rows.Next() {
		var p LLMConfig
		if err := rows.Scan(&p.Name, &p.ProviderType, &p.APIKey, &p.BaseURL, &p.MaxTokens, &p.DefaultModel, &p.FavouriteModels); err != nil {
			return nil, err
		}
		if decrypted, err := s.decrypt(p.APIKey); err == nil {
			p.APIKey = decrypted
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetLLMProvider retrieves a single LLM provider by name.
func (s *Store) GetLLMProvider(ctx context.Context, name string) (*LLMConfig, error) {
	var p LLMConfig
	err := s.pool.QueryRow(ctx, `
		SELECT name, provider_type, api_key, base_url, max_tokens, default_model, favourite_models
		FROM llm_providers WHERE name = $1
	`, name).Scan(&p.Name, &p.ProviderType, &p.APIKey, &p.BaseURL, &p.MaxTokens, &p.DefaultModel, &p.FavouriteModels)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting LLM provider %q: %w", name, err)
	}
	if decrypted, err := s.decrypt(p.APIKey); err == nil {
		p.APIKey = decrypted
	}
	return &p, nil
}

// UpsertLLMProvider inserts or updates an LLM provider.
func (s *Store) UpsertLLMProvider(ctx context.Context, p LLMConfig) error {
	encKey := p.APIKey
	if p.APIKey != "" {
		var err error
		encKey, err = s.encrypt(p.APIKey)
		if err != nil {
			return fmt.Errorf("encrypting api_key for %q: %w", p.Name, err)
		}
	}
	favourites := p.FavouriteModels
	if favourites == nil {
		favourites = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO llm_providers (name, provider_type, api_key, base_url, max_tokens, default_model, favourite_models)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET
			provider_type = EXCLUDED.provider_type,
			api_key = EXCLUDED.api_key,
			base_url = EXCLUDED.base_url,
			max_tokens = EXCLUDED.max_tokens,
			default_model = EXCLUDED.default_model,
			favourite_models = EXCLUDED.favourite_models
	`, p.Name, p.ProviderType, encKey, p.BaseURL, p.MaxTokens, p.DefaultModel, favourites)
	if err != nil {
		return fmt.Errorf("upserting LLM provider %q: %w", p.Name, err)
	}
	return nil
}

// DeleteLLMProvider removes an LLM provider by name.
func (s *Store) DeleteLLMProvider(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM llm_providers WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting LLM provider %q: %w", name, err)
	}
	return nil
}

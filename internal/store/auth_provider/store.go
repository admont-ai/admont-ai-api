package auth_provider

import (
	"context"
	"fmt"
	"strings"

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

// --- Auth Providers ---

// ListAuthProviders returns all auth providers from the database.
func (s *Store) ListAuthProviders(ctx context.Context) ([]AuthProvider, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, client_id, client_secret, tenant_id, issuer_url, domain, scopes
		FROM auth_providers ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing auth providers: %w", err)
	}
	defer rows.Close()

	var providers []AuthProvider
	for rows.Next() {
		var p AuthProvider
		var scopesStr string
		if err := rows.Scan(&p.Name, &p.ClientID, &p.ClientSecret,
			&p.TenantID, &p.IssuerURL, &p.Domain, &scopesStr); err != nil {
			return nil, err
		}
		if scopesStr != "" {
			p.Scopes = strings.Split(scopesStr, ",")
		}
		if decrypted, err := s.decrypt(p.ClientSecret); err == nil {
			p.ClientSecret = decrypted
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetAuthProviderID returns the database ID for an auth provider by name.
func (s *Store) GetAuthProviderID(ctx context.Context, name string) (int, error) {
	var id int
	err := s.pool.QueryRow(ctx, `SELECT id FROM auth_providers WHERE name = $1`, name).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("auth provider %q not found", name)
	}
	if err != nil {
		return 0, fmt.Errorf("getting auth provider ID for %q: %w", name, err)
	}
	return id, nil
}

// GetAuthProvider retrieves a single auth provider by name.
func (s *Store) GetAuthProvider(ctx context.Context, name string) (*AuthProvider, error) {
	var p AuthProvider
	var scopesStr string
	err := s.pool.QueryRow(ctx, `
		SELECT name, client_id, client_secret, tenant_id, issuer_url, domain, scopes
		FROM auth_providers WHERE name = $1
	`, name).Scan(&p.Name, &p.ClientID, &p.ClientSecret,
		&p.TenantID, &p.IssuerURL, &p.Domain, &scopesStr)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting auth provider %q: %w", name, err)
	}
	if scopesStr != "" {
		p.Scopes = strings.Split(scopesStr, ",")
	}
	if decrypted, err := s.decrypt(p.ClientSecret); err == nil {
		p.ClientSecret = decrypted
	}
	return &p, nil
}

// UpsertAuthProvider inserts or updates an auth provider.
func (s *Store) UpsertAuthProvider(ctx context.Context, p AuthProvider) error {
	encSecret, err := s.encrypt(p.ClientSecret)
	if err != nil {
		return fmt.Errorf("encrypting client_secret for %q: %w", p.Name, err)
	}
	scopesStr := strings.Join(p.Scopes, ",")
	_, err = s.pool.Exec(ctx, `
		INSERT INTO auth_providers (name, client_id, client_secret, tenant_id, issuer_url, domain, scopes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET
			client_id = EXCLUDED.client_id,
			client_secret = EXCLUDED.client_secret,
			tenant_id = EXCLUDED.tenant_id,
			issuer_url = EXCLUDED.issuer_url,
			domain = EXCLUDED.domain,
			scopes = EXCLUDED.scopes
	`, p.Name, p.ClientID, encSecret, p.TenantID, p.IssuerURL, p.Domain, scopesStr)
	if err != nil {
		return fmt.Errorf("upserting auth provider %q: %w", p.Name, err)
	}
	return nil
}

// DeleteAuthProvider removes an auth provider by name.
func (s *Store) DeleteAuthProvider(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_providers WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("deleting auth provider %q: %w", name, err)
	}
	return nil
}

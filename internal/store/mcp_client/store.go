package mcp_client

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateRegisteredClient persists a newly dynamically-registered MCP OAuth
// client (RFC 7591). redirectURIs may be empty — an empty list means "any
// redirect_uri is accepted for this client", matching the pre-existing
// in-memory behavior this store replaces.
func (s *Store) CreateRegisteredClient(ctx context.Context, clientID string, redirectURIs []string) error {
	if redirectURIs == nil {
		redirectURIs = []string{}
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO mcp_registered_clients (client_id, redirect_uris) VALUES ($1, $2)`,
		clientID, redirectURIs)
	if err != nil {
		return fmt.Errorf("creating MCP registered client %q: %w", clientID, err)
	}
	return nil
}

// GetRegisteredClient returns the client, or (nil, nil) if clientID isn't registered.
func (s *Store) GetRegisteredClient(ctx context.Context, clientID string) (*RegisteredClient, error) {
	var c RegisteredClient
	c.ClientID = clientID
	err := s.pool.QueryRow(ctx,
		`SELECT redirect_uris, created_at FROM mcp_registered_clients WHERE client_id = $1`,
		clientID).Scan(&c.RedirectURIs, &c.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting MCP registered client %q: %w", clientID, err)
	}
	return &c, nil
}

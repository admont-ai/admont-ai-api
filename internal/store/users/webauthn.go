package users

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WebAuthnCredential is a stored passkey credential for an internal user.
type WebAuthnCredential struct {
	ID              int
	UserID          int
	CredentialID    []byte
	PublicKey       []byte
	AttestationType string
	Transports      []string
	AAGUID          []byte
	SignCount       int64
	BackupEligible  bool
	BackupState     bool
	Name            string
	CreatedAt       string
	LastUsedAt      string
}

const webauthnColumns = `id, user_id, credential_id, public_key, attestation_type, transports, aaguid,
	sign_count, backup_eligible, backup_state, name, created_at, last_used_at`

func scanWebAuthnCredential(row pgx.Row) (*WebAuthnCredential, error) {
	var c WebAuthnCredential
	var aaguid []byte
	var createdAt time.Time
	var lastUsedAt *time.Time
	if err := row.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.AttestationType, &c.Transports,
		&aaguid, &c.SignCount, &c.BackupEligible, &c.BackupState, &c.Name, &createdAt, &lastUsedAt); err != nil {
		return nil, err
	}
	c.AAGUID = aaguid
	c.CreatedAt = createdAt.UTC().Format(timeFormat)
	if lastUsedAt != nil {
		c.LastUsedAt = lastUsedAt.UTC().Format(timeFormat)
	}
	return &c, nil
}

// AddWebAuthnCredential stores a new passkey credential for an internal user.
func (s *Store) AddWebAuthnCredential(ctx context.Context, email string, c WebAuthnCredential) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webauthn_credentials
			(user_id, credential_id, public_key, attestation_type, transports, aaguid, sign_count, backup_eligible, backup_state, name)
		VALUES (`+internalUserID+`, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, email, c.CredentialID, c.PublicKey, c.AttestationType, c.Transports, c.AAGUID,
		c.SignCount, c.BackupEligible, c.BackupState, c.Name)
	if err != nil {
		return fmt.Errorf("adding webauthn credential for %q: %w", email, err)
	}
	return nil
}

// ListWebAuthnCredentials returns all passkey credentials for an internal user (by email).
func (s *Store) ListWebAuthnCredentials(ctx context.Context, email string) ([]WebAuthnCredential, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webauthnColumns+`
		FROM webauthn_credentials
		WHERE user_id = `+internalUserID+`
		ORDER BY created_at`, email)
	if err != nil {
		return nil, fmt.Errorf("listing webauthn credentials for %q: %w", email, err)
	}
	defer rows.Close()
	return collectWebAuthnCredentials(rows)
}

// ListWebAuthnCredentialsByUserID returns all passkey credentials for a user id.
func (s *Store) ListWebAuthnCredentialsByUserID(ctx context.Context, userID int) ([]WebAuthnCredential, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webauthnColumns+`
		FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing webauthn credentials for user %d: %w", userID, err)
	}
	defer rows.Close()
	return collectWebAuthnCredentials(rows)
}

func collectWebAuthnCredentials(rows pgx.Rows) ([]WebAuthnCredential, error) {
	var creds []WebAuthnCredential
	for rows.Next() {
		c, err := scanWebAuthnCredential(rows)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *c)
	}
	return creds, rows.Err()
}

// GetWebAuthnCredentialByID returns a single credential by its raw credential id, or nil if not found.
func (s *Store) GetWebAuthnCredentialByID(ctx context.Context, credID []byte) (*WebAuthnCredential, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+webauthnColumns+`
		FROM webauthn_credentials WHERE credential_id = $1`, credID)
	c, err := scanWebAuthnCredential(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting webauthn credential: %w", err)
	}
	return c, nil
}

// UpdateWebAuthnCredentialOnLogin persists the post-assertion sign count and
// backup state and stamps last_used_at.
func (s *Store) UpdateWebAuthnCredentialOnLogin(ctx context.Context, credID []byte, signCount int64, backupState bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webauthn_credentials
		SET sign_count = $2, backup_state = $3, last_used_at = NOW()
		WHERE credential_id = $1`, credID, signCount, backupState)
	if err != nil {
		return fmt.Errorf("updating webauthn credential: %w", err)
	}
	return nil
}

// RenameWebAuthnCredential renames a credential owned by the given internal user.
func (s *Store) RenameWebAuthnCredential(ctx context.Context, email string, id int, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE webauthn_credentials SET name = $3
		WHERE id = $2 AND user_id = `+internalUserID, email, id, name)
	if err != nil {
		return fmt.Errorf("renaming webauthn credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("webauthn credential not found")
	}
	return nil
}

// DeleteWebAuthnCredential removes a credential owned by the given internal user.
func (s *Store) DeleteWebAuthnCredential(ctx context.Context, email string, id int) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM webauthn_credentials
		WHERE id = $2 AND user_id = `+internalUserID, email, id)
	if err != nil {
		return fmt.Errorf("deleting webauthn credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("webauthn credential not found")
	}
	return nil
}

package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetSetting retrieves a single setting value by key. Returns empty string if not found.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting setting %q: %w", key, err)
	}
	return value, nil
}

// SetSetting inserts or updates a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("setting %q: %w", key, err)
	}
	return nil
}

// GetEncryptedSetting retrieves a setting and decrypts it.
// Non-prefixed values are returned as-is for backward compatibility.
func (s *Store) GetEncryptedSetting(ctx context.Context, key string) (string, error) {
	stored, err := s.GetSetting(ctx, key)
	if err != nil || stored == "" {
		return stored, err
	}
	return s.decryptSecret(stored)
}

// SetEncryptedSetting encrypts a value and stores it as a setting.
func (s *Store) SetEncryptedSetting(ctx context.Context, key, value string) error {
	if value == "" {
		return s.SetSetting(ctx, key, value)
	}
	encrypted, err := s.encryptSecret(value)
	if err != nil {
		return fmt.Errorf("encrypting setting %q: %w", key, err)
	}
	return s.SetSetting(ctx, key, encrypted)
}

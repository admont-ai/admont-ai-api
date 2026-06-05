package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

const encPrefix = "enc:"

// SetEncryptionKey derives an AES-256 key from the given secret using SHA-256.
func (s *Store) SetEncryptionKey(secret string) {
	h := sha256.Sum256([]byte(secret))
	s.encryptionKey = h[:]
}

// SetEncryptionKeyRaw sets the encryption key directly from raw bytes (must be 32 bytes).
func (s *Store) SetEncryptionKeyRaw(key []byte) {
	s.encryptionKey = key
}

// EncryptionKey returns the current encryption key (may be nil).
func (s *Store) EncryptionKey() []byte {
	return s.encryptionKey
}

// encryptSecret encrypts a plaintext secret and returns it with the "enc:" prefix.
// Empty strings pass through unchanged.
func (s *Store) encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	encrypted, err := s.Encrypt(plaintext)
	if err != nil {
		return "", err
	}
	return encPrefix + encrypted, nil
}

// decryptSecret decrypts a stored secret. Values without the "enc:" prefix are
// returned as-is for backward compatibility with pre-encryption plaintext.
func (s *Store) decryptSecret(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	return s.Decrypt(strings.TrimPrefix(stored, encPrefix))
}

// Encrypt encrypts plaintext using AES-256-GCM and returns hex-encoded ciphertext.
func (s *Store) Encrypt(plaintext string) (string, error) {
	if len(s.encryptionKey) == 0 {
		return "", fmt.Errorf("encryption key not set")
	}

	block, err := aes.NewCipher(s.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

// Decrypt decrypts hex-encoded AES-256-GCM ciphertext and returns plaintext.
func (s *Store) Decrypt(encoded string) (string, error) {
	if len(s.encryptionKey) == 0 {
		return "", fmt.Errorf("encryption key not set")
	}

	ciphertext, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding hex: %w", err)
	}

	block, err := aes.NewCipher(s.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}

	return string(plaintext), nil
}

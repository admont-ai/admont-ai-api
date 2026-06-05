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

	"golang.org/x/crypto/hkdf"
)

const encPrefix = "enc:"

// v2 prefix distinguishes HKDF-derived keys from legacy SHA-256-derived keys.
const encPrefixV2 = "enc2:"

// SetEncryptionKey derives an AES-256 key from the given secret using HKDF-SHA256.
// A legacy key (SHA-256) is also retained so that data encrypted with the old
// derivation can still be decrypted and transparently re-encrypted on next write.
func (s *Store) SetEncryptionKey(secret string) {
	// HKDF-derived primary key (new format).
	r := hkdf.New(sha256.New, []byte(secret), nil, []byte("admont-encryption-key-v2"))
	key := make([]byte, 32)
	io.ReadFull(r, key)
	s.encryptionKey = key

	// Legacy SHA-256-derived key for decrypting old data.
	h := sha256.Sum256([]byte(secret))
	s.legacyEncryptionKey = h[:]
}

// SetEncryptionKeyRaw sets the encryption key directly from raw bytes (must be 32 bytes).
func (s *Store) SetEncryptionKeyRaw(key []byte) {
	s.encryptionKey = key
	s.legacyEncryptionKey = nil
}

// EncryptionKey returns the current encryption key (may be nil).
func (s *Store) EncryptionKey() []byte {
	return s.encryptionKey
}

// encryptSecret encrypts a plaintext secret and returns it with the "enc2:" prefix.
// Empty strings pass through unchanged.
func (s *Store) encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	encrypted, err := s.Encrypt(plaintext)
	if err != nil {
		return "", err
	}
	return encPrefixV2 + encrypted, nil
}

// decryptSecret decrypts a stored secret. Supports both "enc2:" (HKDF) and
// legacy "enc:" (SHA-256) prefixed values. Values without any prefix are
// returned as-is for backward compatibility with pre-encryption plaintext.
func (s *Store) decryptSecret(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if strings.HasPrefix(stored, encPrefixV2) {
		return s.Decrypt(strings.TrimPrefix(stored, encPrefixV2))
	}
	if strings.HasPrefix(stored, encPrefix) {
		return s.decryptLegacy(strings.TrimPrefix(stored, encPrefix))
	}
	return stored, nil
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
	return decryptWithKey(encoded, s.encryptionKey)
}

// decryptLegacy decrypts data encrypted with the legacy SHA-256-derived key.
func (s *Store) decryptLegacy(encoded string) (string, error) {
	key := s.legacyEncryptionKey
	if len(key) == 0 {
		key = s.encryptionKey
	}
	return decryptWithKey(encoded, key)
}

func decryptWithKey(encoded string, key []byte) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("encryption key not set")
	}

	ciphertext, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decoding hex: %w", err)
	}

	block, err := aes.NewCipher(key)
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

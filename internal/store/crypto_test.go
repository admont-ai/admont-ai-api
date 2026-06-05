package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStoreWithKey() *Store {
	s := &Store{}
	s.SetEncryptionKey("test-secret-for-encryption")
	return s
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	s := newTestStoreWithKey()

	tests := []string{
		"hello world",
		"sk-ant-api03-xxxxxxxxxxxx",
		"",
		"a",
		"unicode: 你好世界 🌍",
		"special chars: !@#$%^&*()_+-=[]{}|;':\",./<>?",
		string(make([]byte, 10000)),
	}

	for _, plaintext := range tests {
		t.Run(plaintext[:min(len(plaintext), 30)], func(t *testing.T) {
			if plaintext == "" {
				encrypted, err := s.Encrypt(plaintext)
				require.NoError(t, err)
				decrypted, err := s.Decrypt(encrypted)
				require.NoError(t, err)
				assert.Equal(t, plaintext, decrypted)
				return
			}
			encrypted, err := s.Encrypt(plaintext)
			require.NoError(t, err)
			assert.NotEqual(t, plaintext, encrypted)

			decrypted, err := s.Decrypt(encrypted)
			require.NoError(t, err)
			assert.Equal(t, plaintext, decrypted)
		})
	}
}

func TestEncrypt_DifferentCiphertextEachTime(t *testing.T) {
	s := newTestStoreWithKey()

	e1, err := s.Encrypt("same input")
	require.NoError(t, err)
	e2, err := s.Encrypt("same input")
	require.NoError(t, err)

	assert.NotEqual(t, e1, e2, "nonce should make each encryption unique")
}

func TestDecrypt_WrongKey(t *testing.T) {
	s1 := &Store{}
	s1.SetEncryptionKey("key-one")

	s2 := &Store{}
	s2.SetEncryptionKey("key-two")

	encrypted, err := s1.Encrypt("secret data")
	require.NoError(t, err)

	_, err = s2.Decrypt(encrypted)
	assert.Error(t, err)
}

func TestEncrypt_NoKeySet(t *testing.T) {
	s := &Store{}

	_, err := s.Encrypt("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "encryption key not set")

	_, err = s.Decrypt("abcd")
	assert.Error(t, err)
}

func TestDecrypt_InvalidHex(t *testing.T) {
	s := newTestStoreWithKey()

	_, err := s.Decrypt("not-valid-hex!")
	assert.Error(t, err)
}

func TestDecrypt_TooShort(t *testing.T) {
	s := newTestStoreWithKey()

	_, err := s.Decrypt("aabb")
	assert.Error(t, err)
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	s := newTestStoreWithKey()

	encrypted, err := s.Encrypt("sensitive data")
	require.NoError(t, err)

	tampered := encrypted[:len(encrypted)-4] + "dead"
	_, err = s.Decrypt(tampered)
	assert.Error(t, err)
}

func TestSetEncryptionKey_DerivesHKDF(t *testing.T) {
	s := &Store{}
	s.SetEncryptionKey("my-secret")
	assert.Len(t, s.EncryptionKey(), 32)
	assert.NotNil(t, s.legacyEncryptionKey)
	assert.Len(t, s.legacyEncryptionKey, 32)
	assert.NotEqual(t, s.encryptionKey, s.legacyEncryptionKey)
}

func TestSetEncryptionKeyRaw(t *testing.T) {
	s := &Store{}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s.SetEncryptionKeyRaw(key)
	assert.Equal(t, key, s.EncryptionKey())
}

func TestEncryptSecret_EmptyPassthrough(t *testing.T) {
	s := newTestStoreWithKey()

	result, err := s.encryptSecret("")
	require.NoError(t, err)
	assert.Equal(t, "", result)
}

func TestEncryptSecret_AddsV2Prefix(t *testing.T) {
	s := newTestStoreWithKey()

	result, err := s.encryptSecret("my-api-key")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(result, "enc2:"))
}

func TestDecryptSecret_WithPrefix(t *testing.T) {
	s := newTestStoreWithKey()

	encrypted, err := s.encryptSecret("my-api-key")
	require.NoError(t, err)

	decrypted, err := s.decryptSecret(encrypted)
	require.NoError(t, err)
	assert.Equal(t, "my-api-key", decrypted)
}

func TestDecryptSecret_WithoutPrefix_BackwardCompat(t *testing.T) {
	s := newTestStoreWithKey()

	decrypted, err := s.decryptSecret("plaintext-legacy-value")
	require.NoError(t, err)
	assert.Equal(t, "plaintext-legacy-value", decrypted)
}

func TestDecryptSecret_EmptyPassthrough(t *testing.T) {
	s := newTestStoreWithKey()

	decrypted, err := s.decryptSecret("")
	require.NoError(t, err)
	assert.Equal(t, "", decrypted)
}

func TestEnsureSearchPath(t *testing.T) {
	tests := []struct {
		name   string
		dsn    string
		schema string
		want   string
	}{
		{
			"adds search_path when missing",
			"postgres://admin:admin@localhost:5433/admont-ai?sslmode=disable",
			"admont_ai",
			"postgres://admin:admin@localhost:5433/admont-ai?search_path=admont_ai&sslmode=disable",
		},
		{
			"preserves existing search_path",
			"postgres://admin:admin@localhost:5433/admont-ai?search_path=custom&sslmode=disable",
			"admont_ai",
			"postgres://admin:admin@localhost:5433/admont-ai?search_path=custom&sslmode=disable",
		},
		{
			"handles DSN without query params",
			"postgres://admin:admin@localhost:5433/admont-ai",
			"admont_ai",
			"postgres://admin:admin@localhost:5433/admont-ai?search_path=admont_ai",
		},
		{
			"handles invalid DSN gracefully",
			"://invalid",
			"admont_ai",
			"://invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureSearchPath(tt.dsn, tt.schema)
			assert.Equal(t, tt.want, got)
		})
	}
}

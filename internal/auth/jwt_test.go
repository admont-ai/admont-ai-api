package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJWTService_GenerateAndValidate(t *testing.T) {
	svc := NewJWTService("test-secret-key", time.Hour)

	token, err := svc.GenerateToken("alice@example.com", "Alice", "sub-123", "google")
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", claims.Email)
	assert.Equal(t, "Alice", claims.Name)
	assert.Equal(t, "google", claims.Provider)
	assert.Equal(t, "google:alice@example.com", claims.Identity)
	assert.Equal(t, "sub-123", claims.Subject)
}

func TestJWTService_IdentityWithoutProvider(t *testing.T) {
	svc := NewJWTService("secret", time.Hour)

	token, err := svc.GenerateToken("bob@example.com", "Bob", "sub-456", "")
	require.NoError(t, err)

	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)
	assert.Equal(t, "bob@example.com", claims.Identity)
	assert.Equal(t, "", claims.Provider)
}

func TestJWTService_ExpiredToken(t *testing.T) {
	svc := NewJWTService("secret", -time.Hour)

	token, err := svc.GenerateToken("expired@example.com", "Expired", "sub", "google")
	require.NoError(t, err)

	_, err = svc.ValidateToken(token)
	assert.Error(t, err)
}

func TestJWTService_WrongSecret(t *testing.T) {
	svc1 := NewJWTService("secret-one", time.Hour)
	svc2 := NewJWTService("secret-two", time.Hour)

	token, err := svc1.GenerateToken("user@example.com", "User", "sub", "github")
	require.NoError(t, err)

	_, err = svc2.ValidateToken(token)
	assert.Error(t, err)
}

func TestJWTService_MalformedToken(t *testing.T) {
	svc := NewJWTService("secret", time.Hour)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"garbage", "not-a-jwt"},
		{"two parts", "header.payload"},
		{"three parts garbage", "a.b.c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ValidateToken(tt.token)
			assert.Error(t, err)
		})
	}
}

func TestJWTService_ClaimsExpiry(t *testing.T) {
	svc := NewJWTService("secret", 2*time.Hour)

	token, err := svc.GenerateToken("user@example.com", "User", "sub", "google")
	require.NoError(t, err)

	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)

	assert.NotNil(t, claims.IssuedAt)
	assert.NotNil(t, claims.ExpiresAt)
	assert.True(t, claims.ExpiresAt.After(claims.IssuedAt.Time))
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), claims.ExpiresAt.Time, 5*time.Second)
}

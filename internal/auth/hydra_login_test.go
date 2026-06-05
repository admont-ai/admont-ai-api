package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginRateLimiter_AllowsBelowThreshold(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      3,
		window:   time.Minute,
	}

	assert.False(t, rl.blocked("1.2.3.4"))
	assert.False(t, rl.record("1.2.3.4"))
	assert.False(t, rl.record("1.2.3.4"))
	assert.False(t, rl.blocked("1.2.3.4"))
}

func TestLoginRateLimiter_BlocksAtThreshold(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      3,
		window:   time.Minute,
	}

	rl.record("1.2.3.4")
	rl.record("1.2.3.4")
	rl.record("1.2.3.4")

	assert.True(t, rl.blocked("1.2.3.4"))
}

func TestLoginRateLimiter_BlocksOnExceed(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      2,
		window:   time.Minute,
	}

	rl.record("1.2.3.4")
	blocked := rl.record("1.2.3.4")
	assert.False(t, blocked)

	blocked = rl.record("1.2.3.4")
	assert.True(t, blocked)
}

func TestLoginRateLimiter_SeparateIPs(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      2,
		window:   time.Minute,
	}

	rl.record("1.1.1.1")
	rl.record("1.1.1.1")
	assert.True(t, rl.blocked("1.1.1.1"))
	assert.False(t, rl.blocked("2.2.2.2"))
}

func TestLoginRateLimiter_WindowExpiry(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      2,
		window:   100 * time.Millisecond,
	}

	rl.record("1.2.3.4")
	rl.record("1.2.3.4")
	assert.True(t, rl.blocked("1.2.3.4"))

	time.Sleep(150 * time.Millisecond)
	assert.False(t, rl.blocked("1.2.3.4"))
}

func TestLoginRateLimiter_Pruned(t *testing.T) {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      5,
		window:   time.Minute,
	}

	now := time.Now()
	rl.attempts["ip"] = []time.Time{
		now.Add(-2 * time.Minute),
		now.Add(-90 * time.Second),
		now.Add(-30 * time.Second),
		now.Add(-10 * time.Second),
	}

	rl.mu.Lock()
	valid := rl.pruned("ip", now)
	rl.mu.Unlock()

	assert.Equal(t, 2, len(valid))
}

func TestPendingToken_CreateAndValidate(t *testing.T) {
	key := []byte("test-signing-key-32-bytes-long!!")
	h := &HydraLoginHandler{signingKey: key}

	token := h.createPendingToken("alice@example.com", "challenge-123")
	assert.NotEmpty(t, token)

	email, challenge, err := h.validatePendingToken(token)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", email)
	assert.Equal(t, "challenge-123", challenge)
}

func TestPendingToken_TamperedSignature(t *testing.T) {
	key := []byte("test-signing-key-32-bytes-long!!")
	h := &HydraLoginHandler{signingKey: key}

	token := h.createPendingToken("alice@example.com", "challenge-123")
	tampered := token[:len(token)-4] + "dead"

	_, _, err := h.validatePendingToken(tampered)
	assert.Error(t, err)
}

func TestPendingToken_WrongKey(t *testing.T) {
	h1 := &HydraLoginHandler{signingKey: []byte("key-one-32-bytes-long-enough!!!!")}
	h2 := &HydraLoginHandler{signingKey: []byte("key-two-32-bytes-long-enough!!!!")}

	token := h1.createPendingToken("alice@example.com", "challenge")
	_, _, err := h2.validatePendingToken(token)
	assert.Error(t, err)
}

func TestPendingToken_MalformedInput(t *testing.T) {
	h := &HydraLoginHandler{signingKey: []byte("test-signing-key-32-bytes-long!!")}

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"one part", "just-one-part"},
		{"two parts", "a|b"},
		{"three parts", "a|b|c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := h.validatePendingToken(tt.token)
			assert.Error(t, err)
		})
	}
}

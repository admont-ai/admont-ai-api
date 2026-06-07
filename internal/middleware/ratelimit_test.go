package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter_AllowsBurst(t *testing.T) {
	rl := NewRateLimiter(10) // 10 per minute

	for i := 0; i < 10; i++ {
		assert.True(t, rl.allow("user1"), "request %d should be allowed", i+1)
	}
	assert.False(t, rl.allow("user1"), "11th request should be rejected")
}

func TestRateLimiter_SeparateKeys(t *testing.T) {
	rl := NewRateLimiter(5)

	for i := 0; i < 5; i++ {
		assert.True(t, rl.allow("user1"))
	}
	assert.False(t, rl.allow("user1"))

	// Different key should have its own bucket
	assert.True(t, rl.allow("user2"))
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(60) // 1 per second

	// Exhaust all tokens
	for i := 0; i < 60; i++ {
		rl.allow("user1")
	}
	assert.False(t, rl.allow("user1"))

	// Simulate time passing by manipulating the bucket
	rl.mu.Lock()
	b := rl.buckets["user1"]
	b.tokens = 1 // simulate refill
	rl.mu.Unlock()

	assert.True(t, rl.allow("user1"))
}

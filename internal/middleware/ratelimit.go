package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type rateBucket struct {
	tokens    float64
	lastFill  time.Time
}

// RateLimiter implements a per-key token bucket rate limiter.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*rateBucket
	rate     float64 // tokens per second
	burst    int     // max tokens
}

// NewRateLimiter creates a rate limiter allowing `perMinute` requests per minute
// with a burst equal to `perMinute`.
func NewRateLimiter(perMinute int) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*rateBucket),
		rate:    float64(perMinute) / 60.0,
		burst:   perMinute,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &rateBucket{tokens: float64(rl.burst), lastFill: now}
		rl.buckets[key] = b
	}

	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for k, b := range rl.buckets {
			if b.lastFill.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit returns Gin middleware that limits requests per key.
// The key is the authenticated user identity if present, otherwise the client IP.
func RateLimit(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, exists := c.Get(CtxUserIdentity)
		if !exists || key == nil || key.(string) == "" {
			key = c.ClientIP()
		}
		if !rl.allow(key.(string)) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}

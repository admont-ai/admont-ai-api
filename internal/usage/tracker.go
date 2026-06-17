// Package usage tracks per-user LLM token consumption in memory and enforces
// configurable daily quotas. Usage is intentionally not persisted: a restart
// (or the daily 00:00 UTC reset) starts every user from zero.
package usage

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DefaultLimitSettingKey is the settings-table key holding the global default
// daily limits (JSON-encoded DailyLimits). A limit of 0 means unlimited.
const DefaultLimitSettingKey = "daily_token_limit_default"

// DailyLimits is a pair of daily token caps. A value of 0 means unlimited.
type DailyLimits struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

// Usage is a snapshot of one key's consumption since the last reset.
type Usage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

// Tracker holds in-memory token usage keyed by user identity (or client IP for
// unauthenticated callers). All methods are safe for concurrent use.
type Tracker struct {
	mu     sync.Mutex
	counts map[string]*Usage
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{counts: make(map[string]*Usage)}
}

// Add increments the input/output usage for a key.
func (t *Tracker) Add(key string, input, output int64) {
	if key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u, ok := t.counts[key]
	if !ok {
		u = &Usage{}
		t.counts[key] = u
	}
	u.Input += input
	u.Output += output
}

// Get returns the current usage for a key (zero if unseen).
func (t *Tracker) Get(key string) Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	if u, ok := t.counts[key]; ok {
		return *u
	}
	return Usage{}
}

// Snapshot returns a copy of all current usage keyed by identity.
func (t *Tracker) Snapshot() map[string]Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Usage, len(t.counts))
	for k, u := range t.counts {
		out[k] = *u
	}
	return out
}

// Reset clears usage for a single key.
func (t *Tracker) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, key)
}

// ResetAll clears usage for every key.
func (t *Tracker) ResetAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts = make(map[string]*Usage)
}

// StartDailyReset runs a goroutine that clears all usage at the next 00:00 UTC
// and every 24h thereafter, until ctx is cancelled.
func (t *Tracker) StartDailyReset(ctx context.Context) {
	go func() {
		for {
			d := untilNextMidnightUTC(time.Now().UTC())
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				t.ResetAll()
				log.Info("daily LLM token usage reset (00:00 UTC)")
			}
		}
	}()
}

// untilNextMidnightUTC returns the duration from now to the next 00:00 UTC.
func untilNextMidnightUTC(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	return next.Sub(now)
}

// --- request-scoped identity (used to attribute usage from the LLM client) ---

type ctxKey struct{}

// WithIdentity stores the usage key (user identity or client IP) on a context so
// the LLM client's usage hook can attribute token consumption to it.
func WithIdentity(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxKey{}, key)
}

// IdentityFrom returns the usage key previously stored with WithIdentity, or "".
func IdentityFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

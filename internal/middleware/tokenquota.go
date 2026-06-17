package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/store"
	"github.com/christianfischer/md-wiki-server/internal/usage"
	"github.com/gin-gonic/gin"
)

// TokenQuota enforces the per-user daily LLM token limit. It must run after a
// JWT auth middleware so the user identity is available. The usage key is the
// authenticated identity, or the client IP for unauthenticated callers. The key
// is stashed on the request context so the LLM client's usage hook can attribute
// consumption to it after the call completes.
//
// The check is a pre-call gate: a request is blocked only once usage has already
// reached the cap, so the call that crosses the threshold is allowed through.
func TokenQuota(tracker *usage.Tracker, st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		identityVal, _ := c.Get(CtxUserIdentity)
		identity, _ := identityVal.(string)

		key := identity
		if key == "" {
			key = c.ClientIP()
		}
		c.Request = c.Request.WithContext(usage.WithIdentity(c.Request.Context(), key))

		inLimit, outLimit := effectiveLimits(c.Request.Context(), st, identity)
		used := tracker.Get(key)
		if (inLimit > 0 && used.Input >= inLimit) || (outLimit > 0 && used.Output >= outLimit) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Daily token limit reached — usage resets at 00:00 UTC",
			})
			return
		}
		c.Next()
	}
}

// effectiveLimits resolves a user's daily caps: the global default, overridden by
// any per-user value. A limit of 0 means unlimited. An empty identity (anonymous)
// gets the global default only.
func effectiveLimits(ctx context.Context, st *store.Store, identity string) (in, out int64) {
	if raw, err := st.GetSetting(ctx, usage.DefaultLimitSettingKey); err == nil && raw != "" {
		var d usage.DailyLimits
		if json.Unmarshal([]byte(raw), &d) == nil {
			in, out = d.Input, d.Output
		}
	}
	if identity == "" {
		return in, out
	}
	provider, email, ok := strings.Cut(identity, ":")
	if !ok {
		return in, out
	}
	u, err := st.Users.GetUser(ctx, provider, email)
	if err != nil || u == nil {
		return in, out
	}
	if u.DailyInputTokenLimit != nil {
		in = *u.DailyInputTokenLimit
	}
	if u.DailyOutputTokenLimit != nil {
		out = *u.DailyOutputTokenLimit
	}
	return in, out
}

package middleware

import (
	"net/http"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Context keys for user identity.
const (
	CtxUserEmail    = "user_email"
	CtxUserName     = "user_name"
	CtxUserSubject  = "user_subject"
	CtxUserProvider = "user_provider"
	CtxUserIdentity = "user_identity"
)

// JWTAuth requires a valid server-issued JWT. Unauthenticated requests are rejected.
func JWTAuth(jwtService *auth.JWTService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		token, found := strings.CutPrefix(authHeader, "Bearer ")
		if !found || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
			return
		}

		claims, err := jwtService.ValidateToken(token)
		if err != nil {
			log.WithError(err).Warn("invalid JWT")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		setUserContext(c, claims)
		c.Next()
	}
}

// AdminCheck requires the authenticated user's identity to be in the admin list.
func AdminCheck(admins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := c.Get(CtxUserIdentity)
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		userIdentity, _ := identity.(string)
		for _, a := range admins {
			if auth.MatchIdentity(a, userIdentity) {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin access required"})
	}
}

// AdminCheckFunc uses a closure to check admin status at request time, enabling runtime updates.
func AdminCheckFunc(isAdmin func(string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := c.Get(CtxUserIdentity)
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		userIdentity, _ := identity.(string)
		if !isAdmin(userIdentity) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin access required"})
			return
		}
		c.Next()
	}
}

// JWTAuthOptional validates a server-issued JWT if present but allows unauthenticated requests through.
func JWTAuthOptional(jwtService *auth.JWTService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		token, found := strings.CutPrefix(authHeader, "Bearer ")
		if !found || token == "" {
			c.Next()
			return
		}

		claims, err := jwtService.ValidateToken(token)
		if err != nil {
			c.Next()
			return
		}

		setUserContext(c, claims)
		c.Next()
	}
}

func setUserContext(c *gin.Context, claims *auth.Claims) {
	c.Set(CtxUserEmail, claims.Email)
	c.Set(CtxUserName, claims.Name)
	c.Set(CtxUserSubject, claims.Subject)
	c.Set(CtxUserProvider, claims.Provider)
	identity := claims.Identity
	if identity == "" {
		// Backward compat for JWTs issued before multi-provider support.
		identity = claims.Email
	}
	c.Set(CtxUserIdentity, identity)
}

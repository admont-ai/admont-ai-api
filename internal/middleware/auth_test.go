package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestJWT() *auth.JWTService {
	return auth.NewJWTService("test-secret-middleware", 24*time.Hour)
}

func newTestToken(t *testing.T, email, name, subject, provider string) string {
	t.Helper()
	svc := newTestJWT()
	token, err := svc.GenerateToken(email, name, subject, provider)
	require.NoError(t, err)
	return token
}

func TestJWTAuth_ValidToken(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "alice@example.com", "Alice", "sub-1", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.GET("/test", func(c *gin.Context) {
		email, _ := c.Get(CtxUserEmail)
		name, _ := c.Get(CtxUserName)
		provider, _ := c.Get(CtxUserProvider)
		identity, _ := c.Get(CtxUserIdentity)
		c.JSON(200, gin.H{
			"email":    email,
			"name":     name,
			"provider": provider,
			"identity": identity,
		})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "alice@example.com")
	assert.Contains(t, w.Body.String(), "google:alice@example.com")
}

func TestJWTAuth_MissingHeader(t *testing.T) {
	svc := newTestJWT()

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing authorization header")
}

func TestJWTAuth_InvalidFormat(t *testing.T) {
	svc := newTestJWT()

	tests := []struct {
		name   string
		header string
	}{
		{"no bearer prefix", "Token abc123"},
		{"empty bearer", "Bearer "},
		{"just bearer", "Bearer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(JWTAuth(svc))
			r.GET("/test", func(c *gin.Context) { c.Status(200) })

			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", tt.header)
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	expired := auth.NewJWTService("test-secret-middleware", -time.Hour)
	token, err := expired.GenerateToken("user@example.com", "User", "sub", "google")
	require.NoError(t, err)

	svc := newTestJWT()
	r := gin.New()
	r.Use(JWTAuth(svc))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTAuth_WrongSecret(t *testing.T) {
	other := auth.NewJWTService("different-secret", time.Hour)
	token, err := other.GenerateToken("user@example.com", "User", "sub", "google")
	require.NoError(t, err)

	svc := newTestJWT()
	r := gin.New()
	r.Use(JWTAuth(svc))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTAuthOptional_NoToken(t *testing.T) {
	svc := newTestJWT()

	r := gin.New()
	r.Use(JWTAuthOptional(svc))
	r.GET("/test", func(c *gin.Context) {
		_, exists := c.Get(CtxUserEmail)
		if exists {
			c.JSON(200, gin.H{"authenticated": true})
		} else {
			c.JSON(200, gin.H{"authenticated": false})
		}
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "false")
}

func TestJWTAuthOptional_ValidToken(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "alice@example.com", "Alice", "sub", "google")

	r := gin.New()
	r.Use(JWTAuthOptional(svc))
	r.GET("/test", func(c *gin.Context) {
		email, exists := c.Get(CtxUserEmail)
		if exists {
			c.JSON(200, gin.H{"email": email})
		} else {
			c.JSON(200, gin.H{"email": nil})
		}
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "alice@example.com")
}

func TestJWTAuthOptional_InvalidToken(t *testing.T) {
	svc := newTestJWT()

	r := gin.New()
	r.Use(JWTAuthOptional(svc))
	r.GET("/test", func(c *gin.Context) {
		_, exists := c.Get(CtxUserEmail)
		c.JSON(200, gin.H{"authenticated": exists})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "false")
}

func TestAdminCheck_AdminAllowed(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "admin@example.com", "Admin", "sub", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.Use(AdminCheck([]string{"google:admin@example.com"}))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAdminCheck_NonAdminRejected(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "user@example.com", "User", "sub", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.Use(AdminCheck([]string{"google:admin@example.com"}))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAdminCheck_BackwardCompatBareEmail(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "admin@example.com", "Admin", "sub", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.Use(AdminCheck([]string{"admin@example.com"}))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAdminCheck_NoAuth(t *testing.T) {
	r := gin.New()
	r.Use(AdminCheck([]string{"admin@example.com"}))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminCheckFunc_Allowed(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "admin@example.com", "Admin", "sub", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.Use(AdminCheckFunc(func(identity string) bool {
		return identity == "google:admin@example.com"
	}))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAdminCheckFunc_Rejected(t *testing.T) {
	svc := newTestJWT()
	token := newTestToken(t, "user@example.com", "User", "sub", "google")

	r := gin.New()
	r.Use(JWTAuth(svc))
	r.Use(AdminCheckFunc(func(identity string) bool { return false }))
	r.GET("/admin", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestSetUserContext_BackwardCompat(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		claims := &auth.Claims{
			Email: "legacy@example.com",
			Name:  "Legacy User",
		}
		setUserContext(c, claims)

		identity, _ := c.Get(CtxUserIdentity)
		c.JSON(200, gin.H{"identity": identity})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "legacy@example.com")
}

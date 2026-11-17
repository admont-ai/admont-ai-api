package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/christianfischer/md-wiki-server/internal/publicurl"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCORSAllowsPublicSameOriginBehindHostRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(cors.New(corsConfig([]string{"https://cross.example.com"}, publicurl.New("", []string{"10.0.0.0/8"}))))
	r.POST("/auth/exchange", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(origin string) int {
		req := httptest.NewRequest("POST", "/auth/exchange", nil)
		req.RemoteAddr = "10.120.1.2:1234"
		req.Host = "docs.internal.example.com" // rewritten by the edge proxy
		req.Header.Set("X-Forwarded-Host", "ui.example.com")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	assert.Equal(t, http.StatusOK, do("https://ui.example.com"), "same public origin")
	assert.Equal(t, http.StatusOK, do("https://cross.example.com"), "configured cross origin")
	assert.Equal(t, http.StatusForbidden, do("https://evil.example.com"), "foreign origin")
	assert.Equal(t, http.StatusForbidden, do("http://ui.example.com"), "scheme mismatch")
}

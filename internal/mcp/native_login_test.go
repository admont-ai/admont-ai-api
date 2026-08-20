package mcp

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func recordRender(fn func(c *gin.Context)) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/mcp/authorize", nil)
	fn(c)
	return w
}

// TestRenderNativeLoginPage_EscapesHTMLInError is the concrete regression
// test for the html/template migration: an error message containing
// HTML-special characters must come out auto-escaped, not injected as raw
// markup. The pre-migration code already escaped manually via
// html.EscapeString, so this test passing does not prove new behavior — it
// proves the switch to auto-escaping didn't regress it.
func TestRenderNativeLoginPage_EscapesHTMLInError(t *testing.T) {
	p := mcpAuthParams{ClientID: "c1", RedirectURI: "https://client.example/cb", State: "s1", CodeChallenge: "chal", CodeChallengeMethod: "S256"}
	w := recordRender(func(c *gin.Context) {
		renderNativeLoginPage(c, p, `Invalid email or password. <script>alert(1)</script>`)
	})

	assert.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "<script>alert(1)</script>")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestRenderNativeLoginPage_HiddenFieldsAndForm(t *testing.T) {
	p := mcpAuthParams{
		ClientID: "my-client", RedirectURI: "https://client.example/cb?x=1&y=2",
		State: "state-value", CodeChallenge: "challenge-value", CodeChallengeMethod: "S256",
	}
	w := recordRender(func(c *gin.Context) {
		renderNativeLoginPage(c, p, "")
	})

	body := w.Body.String()
	assert.Contains(t, body, `action="/mcp/login"`)
	assert.Contains(t, body, `name="client_id" value="my-client"`)
	assert.Contains(t, body, `name="state" value="state-value"`)
	assert.Contains(t, body, `name="code_challenge" value="challenge-value"`)
	assert.Contains(t, body, `name="code_challenge_method" value="S256"`)
	// & must come out escaped in the attribute value.
	assert.Contains(t, body, `name="redirect_uri" value="https://client.example/cb?x=1&amp;y=2"`)
	// No error message supplied -> no error block rendered.
	assert.NotContains(t, body, `class="error"`)
}

func TestRenderNativeTOTPPage_IncludesPendingToken(t *testing.T) {
	p := mcpAuthParams{ClientID: "c1", RedirectURI: "https://client.example/cb", CodeChallenge: "chal", CodeChallengeMethod: "S256"}
	w := recordRender(func(c *gin.Context) {
		renderNativeTOTPPage(c, p, "pending-token-123", "Invalid code. Please try again.")
	})

	body := w.Body.String()
	assert.Contains(t, body, `name="pending_token" value="pending-token-123"`)
	assert.Contains(t, body, `Invalid code. Please try again.`)
	assert.Contains(t, body, `name="step" value="totp"`)
}

func TestRenderProviderChooser_OneLinkPerProviderWithLabels(t *testing.T) {
	s := &Server{}
	w := recordRender(func(c *gin.Context) {
		c.Request = httptest.NewRequest("GET", "/mcp/authorize?redirect_uri=https://client.example/cb&code_challenge=abc", nil)
		s.renderProviderChooser(c, mcpAuthParams{}, []string{"internal", "google"})
	})

	body := w.Body.String()
	assert.Contains(t, body, `class="btn"`)
	assert.Contains(t, body, "Internal Account")
	assert.Contains(t, body, "provider=internal")
	assert.Contains(t, body, "provider=google")
	// Original query params are preserved alongside the added provider param.
	assert.Contains(t, body, "redirect_uri=")
	assert.Contains(t, body, "code_challenge=abc")
}

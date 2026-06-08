package mcp

import (
	"fmt"
	"html"
	"net/http"

	"github.com/christianfischer/md-wiki-server/internal/auth"
	"github.com/gin-gonic/gin"
)

// mcpAuthParams carries the OAuth authorization parameters through the native
// login form so the MCP authorization code can be issued after authentication.
type mcpAuthParams struct {
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// hiddenFields renders the MCP auth params as escaped hidden form inputs.
func (p mcpAuthParams) hiddenFields() string {
	f := func(name, val string) string {
		return `<input type="hidden" name="` + name + `" value="` + html.EscapeString(val) + `">`
	}
	return f("client_id", p.ClientID) +
		f("redirect_uri", p.RedirectURI) +
		f("state", p.State) +
		f("code_challenge", p.CodeChallenge) +
		f("code_challenge_method", p.CodeChallengeMethod)
}

const mcpLoginStyle = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, -apple-system, sans-serif; background: #f5f5f5; display: flex; justify-content: center; align-items: center; min-height: 100vh; }
  .card { background: #fff; border-radius: 12px; box-shadow: 0 2px 8px rgba(0,0,0,.1); padding: 2rem; width: 100%; max-width: 380px; }
  h1 { font-size: 1.4rem; margin-bottom: 1.25rem; text-align: center; color: #222; }
  label { display: block; font-size: .85rem; color: #555; margin-bottom: .25rem; }
  input[type=email], input[type=password], input[type=text] { width: 100%; padding: .6rem; border: 1px solid #ddd; border-radius: 6px; font-size: 1rem; margin-bottom: 1rem; }
  input:focus { outline: none; border-color: #2563eb; box-shadow: 0 0 0 2px rgba(37,99,235,.2); }
  button { width: 100%; padding: .65rem; background: #2563eb; color: #fff; border: none; border-radius: 6px; font-size: 1rem; cursor: pointer; }
  button:hover { background: #1d4ed8; }
  .error { color: #c0392b; font-size: .85rem; text-align: center; margin-bottom: 1rem; }
`

func errorBlock(msg string) string {
	if msg == "" {
		return ""
	}
	return `<p class="error">` + html.EscapeString(msg) + `</p>`
}

// renderNativeLoginPage shows the MCP password login form.
func renderNativeLoginPage(c *gin.Context, p mcpAuthParams, errMsg string) {
	body := fmt.Sprintf(`<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Admont-AI Sign In</title><style>%s</style></head>
<body><div class="card">
  <h1>Admont-AI Sign In</h1>
  %s
  <form method="POST" action="/mcp/login">
    %s
    <input type="hidden" name="step" value="password">
    <label for="email">Email</label>
    <input type="email" id="email" name="email" placeholder="you@example.com" required autofocus>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" required>
    <button type="submit">Sign In</button>
  </form>
</div></body></html>`, mcpLoginStyle, errorBlock(errMsg), p.hiddenFields())
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// renderNativeTOTPPage shows the MCP 2FA step.
func renderNativeTOTPPage(c *gin.Context, p mcpAuthParams, pendingToken, errMsg string) {
	body := fmt.Sprintf(`<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Admont-AI 2FA</title><style>%s</style></head>
<body><div class="card">
  <h1>Two-Factor Authentication</h1>
  %s
  <form method="POST" action="/mcp/login">
    %s
    <input type="hidden" name="step" value="totp">
    <input type="hidden" name="pending_token" value="%s">
    <label for="totp_code">Authenticator or recovery code</label>
    <input type="text" id="totp_code" name="totp_code" inputmode="text" autocomplete="one-time-code" required autofocus>
    <button type="submit">Verify</button>
  </form>
</div></body></html>`, mcpLoginStyle, errorBlock(errMsg), p.hiddenFields(), html.EscapeString(pendingToken))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}

// mcpLogin handles the native internal-user login form (password and TOTP steps)
// for the MCP browser OAuth flow. On success it issues the MCP authorization code.
func (s *Server) mcpLogin(c *gin.Context) {
	if s.authn == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "internal auth not available"})
		return
	}

	p := mcpAuthParams{
		ClientID:            c.PostForm("client_id"),
		RedirectURI:         c.PostForm("redirect_uri"),
		State:               c.PostForm("state"),
		CodeChallenge:       c.PostForm("code_challenge"),
		CodeChallengeMethod: c.PostForm("code_challenge_method"),
	}
	if p.CodeChallengeMethod == "" {
		p.CodeChallengeMethod = "S256"
	}
	if p.CodeChallenge == "" || !s.validateClientRedirectURI(p.ClientID, p.RedirectURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid client_id, redirect_uri, or code_challenge"})
		return
	}

	ip := c.ClientIP()
	if s.authn.Blocked(ip) {
		renderNativeLoginPage(c, p, "Too many failed attempts. Please try again later.")
		return
	}
	ctx := c.Request.Context()

	if c.PostForm("step") == "totp" {
		pendingToken := c.PostForm("pending_token")
		email, err := s.authn.ValidatePendingToken(pendingToken)
		if err != nil {
			renderNativeLoginPage(c, p, "Session expired. Please log in again.")
			return
		}
		if err := s.authn.VerifyTOTP(ctx, ip, email, c.PostForm("totp_code")); err != nil {
			renderNativeTOTPPage(c, p, pendingToken, "Invalid code. Please try again.")
			return
		}
		s.issueMCPCode(c, p.RedirectURI, p.State, p.CodeChallenge, p.CodeChallengeMethod,
			email, s.authn.DisplayName(ctx, email), "", "internal")
		return
	}

	// Password step.
	email := c.PostForm("email")
	password := c.PostForm("password")
	if email == "" || password == "" {
		renderNativeLoginPage(c, p, "Email and password are required.")
		return
	}
	user, err := s.authn.VerifyPassword(ctx, ip, email, password)
	if err != nil {
		msg := "Invalid email or password."
		if err == auth.ErrAccountSuspended {
			msg = "Your account has been suspended."
		}
		renderNativeLoginPage(c, p, msg)
		return
	}
	if user.TOTPEnabled {
		renderNativeTOTPPage(c, p, s.authn.CreatePendingToken(email), "")
		return
	}
	name := email
	if user.FirstName != "" || user.LastName != "" {
		name = user.FirstName
		if user.LastName != "" {
			if name != "" {
				name += " "
			}
			name += user.LastName
		}
	}
	s.issueMCPCode(c, p.RedirectURI, p.State, p.CodeChallenge, p.CodeChallengeMethod, email, name, "", "internal")
}

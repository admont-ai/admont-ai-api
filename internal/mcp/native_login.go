package mcp

import (
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

// loginView is the template data for login.html and totp.html — both share
// the hidden-fields and error-block partials, which read these field names.
type loginView struct {
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Error               string
	PendingToken        string // only used by totp.html
}

func newLoginView(p mcpAuthParams, errMsg string) loginView {
	return loginView{
		ClientID:            p.ClientID,
		RedirectURI:         p.RedirectURI,
		State:               p.State,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Error:               errMsg,
	}
}

// renderNativeLoginPage shows the MCP password login form.
func renderNativeLoginPage(c *gin.Context, p mcpAuthParams, errMsg string) {
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = mcpTemplates.ExecuteTemplate(c.Writer, "login.html", newLoginView(p, errMsg))
}

// renderNativeTOTPPage shows the MCP 2FA step.
func renderNativeTOTPPage(c *gin.Context, p mcpAuthParams, pendingToken, errMsg string) {
	view := newLoginView(p, errMsg)
	view.PendingToken = pendingToken
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = mcpTemplates.ExecuteTemplate(c.Writer, "totp.html", view)
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

package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// displayName builds a human-friendly name from a user, falling back to email.
func displayName(u *users.UserEntry, email string) string {
	parts := make([]string, 0, 2)
	if u != nil && u.FirstName != "" {
		parts = append(parts, u.FirstName)
	}
	if u != nil && u.LastName != "" {
		parts = append(parts, u.LastName)
	}
	if len(parts) == 0 {
		return email
	}
	return strings.Join(parts, " ")
}

// issueTokens mints an access + refresh token for an internal user and writes
// the JSON response.
func (h *Handler) issueTokens(c *gin.Context, email, name string) {
	token, err := h.jwt.GenerateToken(email, name, "", "internal")
	if err != nil {
		log.WithError(err).Error("failed to generate JWT")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	refresh, err := h.jwt.GenerateRefreshToken(email, "internal")
	if err != nil {
		log.WithError(err).Error("failed to generate refresh token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "refresh_token": refresh})
}

// InternalLogin authenticates an internal user with username/email + password.
// If 2FA is enabled it returns {totp_required:true, pending_token}; otherwise
// it returns {token, refresh_token}.
func (h *Handler) InternalLogin(c *gin.Context) {
	if h.authn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "internal auth disabled"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Username == "" || body.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	ip := c.ClientIP()
	if h.authn.Blocked(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed attempts; try again later"})
		return
	}

	user, err := h.authn.VerifyPassword(c.Request.Context(), ip, body.Username, body.Password)
	if err != nil {
		switch err {
		case ErrInvalidCredentials:
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		case ErrAccountSuspended:
			c.JSON(http.StatusForbidden, gin.H{"error": "account suspended"})
		default:
			log.WithError(err).Error("internal login failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}

	email := user.Email
	if user.TOTPEnabled {
		c.JSON(http.StatusOK, gin.H{"totp_required": true, "pending_token": h.authn.CreatePendingToken(email)})
		return
	}
	h.issueOrRequireReset(c, user)
}

// InternalTOTP completes a login by verifying a TOTP or recovery code against
// a pending token issued by InternalLogin.
func (h *Handler) InternalTOTP(c *gin.Context) {
	if h.authn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "internal auth disabled"})
		return
	}
	var body struct {
		PendingToken string `json:"pending_token"`
		Code         string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.PendingToken == "" || body.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pending_token and code are required"})
		return
	}

	ip := c.ClientIP()
	if h.authn.Blocked(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed attempts; try again later"})
		return
	}

	email, err := h.authn.ValidatePendingToken(body.PendingToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired; please log in again"})
		return
	}
	if err := h.authn.VerifyTOTP(c.Request.Context(), ip, email, body.Code); err != nil {
		if err == ErrInvalidTOTP || err == ErrPendingToken {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid code"})
			return
		}
		log.WithError(err).Error("TOTP verification failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	user, _ := h.authn.store.Users.GetInternalUser(c.Request.Context(), email)
	h.issueOrRequireReset(c, user)
}

// InternalSignup creates the first internal user (super admin) and logs them in.
// Only available while no internal users exist.
func (h *Handler) InternalSignup(c *gin.Context) {
	if h.authn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "internal auth disabled"})
		return
	}
	var body struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Username == "" || body.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	err := h.authn.Signup(c.Request.Context(), body.Username, body.Password, body.FirstName, body.LastName)
	switch {
	case err == nil:
		log.WithField("identity", "internal:"+body.Username).Info("first internal user created")
		h.issueTokens(c, body.Username, displayName(&users.UserEntry{FirstName: body.FirstName, LastName: body.LastName}, body.Username))
	case errors.Is(err, ErrWeakPassword):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrSignupClosed):
		c.JSON(http.StatusForbidden, gin.H{"error": "signup is no longer available; ask an administrator to add your account"})
	default:
		log.WithError(err).Error("internal signup failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	}
}

// issueOrRequireReset issues tokens for a fully-authenticated user, unless their
// password is expired — in which case no normal token is issued. Instead a
// short-lived, single-purpose reset token is returned so the client must set a
// new password (via ResetPassword) before gaining any access.
func (h *Handler) issueOrRequireReset(c *gin.Context, user *users.UserEntry) {
	if user != nil && user.PasswordExpired {
		c.JSON(http.StatusOK, gin.H{
			"password_reset_required": true,
			"reset_token":             h.authn.CreateResetToken(user.Email),
		})
		return
	}
	email := ""
	if user != nil {
		email = user.Email
	}
	h.issueTokens(c, email, displayName(user, email))
}

// ResetPassword completes a forced password reset: it verifies the reset token
// issued at login, applies the new password (enforcing complexity), clears the
// expired flag, and only then issues normal tokens.
func (h *Handler) ResetPassword(c *gin.Context) {
	if h.authn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "internal auth disabled"})
		return
	}
	var body struct {
		ResetToken  string `json:"reset_token"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ResetToken == "" || body.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reset_token and new_password are required"})
		return
	}

	ip := c.ClientIP()
	if h.authn.Blocked(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts; try again later"})
		return
	}

	email, err := h.authn.ValidateResetToken(body.ResetToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session expired; please log in again"})
		return
	}
	if err := h.authn.ResetExpiredPassword(c.Request.Context(), email, body.NewPassword); err != nil {
		if errors.Is(err, ErrWeakPassword) || errors.Is(err, ErrPasswordReused) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		log.WithError(err).Error("password reset failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Refresh the admin user cache so password_expired no longer shows as set.
	h.notifyUserChange()

	user, _ := h.authn.store.Users.GetInternalUser(c.Request.Context(), email)
	h.issueTokens(c, email, displayName(user, email))
}

// InternalSignupStatus reports whether first-user signup is still open, so the
// SPA can show a signup vs. login screen.
func (h *Handler) InternalSignupStatus(c *gin.Context) {
	if h.authn == nil {
		c.JSON(http.StatusOK, gin.H{"signup_open": false, "enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"signup_open": h.authn.SignupOpen(c.Request.Context()), "enabled": true})
}

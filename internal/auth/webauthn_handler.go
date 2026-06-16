package auth

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// --- Passkey login (unauthenticated, discoverable) ---

// WebAuthnLoginBegin returns discoverable-login assertion options and a session id.
func (h *Handler) WebAuthnLoginBegin(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	options, sessionID, err := h.webauthn.BeginLogin()
	if err != nil {
		log.WithError(err).Error("webauthn login begin failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"options": options, "session_id": sessionID})
}

// WebAuthnLoginFinish verifies the assertion and, on success, issues tokens
// directly — a user-verified passkey is multi-factor, so TOTP is skipped.
func (h *Handler) WebAuthnLoginFinish(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	ip := c.ClientIP()
	if h.authn != nil && h.authn.Blocked(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed attempts; try again later"})
		return
	}
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	email, err := h.webauthn.FinishLogin(c.Request.Context(), sessionID, c.Request)
	if err != nil {
		if err == ErrAccountSuspended {
			c.JSON(http.StatusForbidden, gin.H{"error": "account suspended"})
			return
		}
		if h.authn != nil {
			h.authn.Record(ip)
		}
		log.WithError(err).Warn("webauthn login failed")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "passkey authentication failed"})
		return
	}

	user, _ := h.users.GetInternalUser(c.Request.Context(), email)
	h.issueTokens(c, email, displayName(user, email))
}

// --- Passkey management (authenticated; mounted under /me) ---

// WebAuthnRegisterBegin returns registration options for the logged-in user.
func (h *Handler) WebAuthnRegisterBegin(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	email := c.GetString("user_email")
	if email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	options, sessionID, err := h.webauthn.BeginRegistration(c.Request.Context(), email)
	if err != nil {
		log.WithError(err).Error("webauthn register begin failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"options": options, "session_id": sessionID})
}

// WebAuthnRegisterFinish verifies the attestation and stores the passkey.
// The credential name is passed via the `name` query parameter (the request
// body carries the WebAuthn attestation JSON).
func (h *Handler) WebAuthnRegisterFinish(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	if c.GetString("user_email") == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}
	if err := h.webauthn.FinishRegistration(c.Request.Context(), sessionID, c.Query("name"), c.Request); err != nil {
		log.WithError(err).Warn("webauthn register finish failed")
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not register passkey"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "passkey added"})
}

// WebAuthnList lists the current user's passkeys.
func (h *Handler) WebAuthnList(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	email := c.GetString("user_email")
	creds, err := h.webauthn.ListCredentials(c.Request.Context(), email)
	if err != nil {
		log.WithError(err).Error("listing passkeys failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	out := make([]gin.H, 0, len(creds))
	for _, cr := range creds {
		out = append(out, gin.H{
			"id":           cr.ID,
			"name":         cr.Name,
			"created_at":   cr.CreatedAt,
			"last_used_at": cr.LastUsedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"passkeys": out})
}

// WebAuthnRename renames one of the current user's passkeys.
func (h *Handler) WebAuthnRename(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := h.webauthn.RenameCredential(c.Request.Context(), c.GetString("user_email"), id, body.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not rename passkey"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "renamed"})
}

// WebAuthnDelete removes one of the current user's passkeys.
func (h *Handler) WebAuthnDelete(c *gin.Context) {
	if h.webauthn == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "passkeys are not enabled"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.webauthn.DeleteCredential(c.Request.Context(), c.GetString("user_email"), id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not delete passkey"})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

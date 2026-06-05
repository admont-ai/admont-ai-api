package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/store"
	storeusers "github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/gin-gonic/gin"
	"github.com/pquerna/otp/totp"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// loginRateLimiter tracks failed login attempts per IP.
type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

func newLoginRateLimiter(max int, window time.Duration) *loginRateLimiter {
	rl := &loginRateLimiter{
		attempts: make(map[string][]time.Time),
		max:      max,
		window:   window,
	}
	go rl.cleanupLoop()
	return rl
}

// record adds a failed attempt and returns true if the IP is now blocked.
func (rl *loginRateLimiter) record(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.attempts[ip] = append(rl.pruned(ip, now), now)
	return len(rl.attempts[ip]) > rl.max
}

// blocked returns true if the IP has exceeded the rate limit.
func (rl *loginRateLimiter) blocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = rl.pruned(ip, time.Now())
	return len(rl.attempts[ip]) >= rl.max
}

// pruned returns only the attempts within the window (caller must hold lock).
func (rl *loginRateLimiter) pruned(ip string, now time.Time) []time.Time {
	cutoff := now.Add(-rl.window)
	var valid []time.Time
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	return valid
}

func (rl *loginRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip := range rl.attempts {
			rl.attempts[ip] = rl.pruned(ip, now)
			if len(rl.attempts[ip]) == 0 {
				delete(rl.attempts, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// HydraLoginHandler handles the Hydra login and consent flow.
type HydraLoginHandler struct {
	adminURL   string
	store      *store.Store
	httpClient *http.Client
	limiter    *loginRateLimiter
	signingKey []byte
}

// NewHydraLoginHandler creates a new handler for the Hydra login/consent flow.
// signingKey is used to HMAC-sign pending TOTP tokens.
func NewHydraLoginHandler(adminURL string, store *store.Store, maxFailed, intervalMins int, signingKey []byte) *HydraLoginHandler {
	return &HydraLoginHandler{
		adminURL:   adminURL,
		store:      store,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		limiter:    newLoginRateLimiter(maxFailed, time.Duration(intervalMins)*time.Minute),
		signingKey: signingKey,
	}
}

const pendingTokenTTL = 5 * time.Minute

// createPendingToken creates an HMAC-signed token: email|challenge|expiry|hmac
func (h *HydraLoginHandler) createPendingToken(email, challenge string) string {
	expiry := strconv.FormatInt(time.Now().Add(pendingTokenTTL).Unix(), 10)
	data := email + "|" + challenge + "|" + expiry
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write([]byte(data))
	sig := hex.EncodeToString(mac.Sum(nil))
	return data + "|" + sig
}

// validatePendingToken validates and parses a pending token. Returns email, challenge, or error.
func (h *HydraLoginHandler) validatePendingToken(token string) (string, string, error) {
	parts := strings.SplitN(token, "|", 4)
	if len(parts) != 4 {
		return "", "", fmt.Errorf("invalid token format")
	}
	email, challenge, expiryStr, sig := parts[0], parts[1], parts[2], parts[3]

	// Verify HMAC.
	data := email + "|" + challenge + "|" + expiryStr
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write([]byte(data))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", "", fmt.Errorf("invalid token signature")
	}

	// Check expiry.
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", "", fmt.Errorf("invalid token expiry")
	}
	if time.Now().Unix() > expiry {
		return "", "", fmt.Errorf("token expired")
	}

	return email, challenge, nil
}

// --- Login ---

type hydraLoginRequest struct {
	Challenge   string `json:"challenge"`
	Skip        bool   `json:"skip"`
	Subject     string `json:"subject"`
	RequestedAt string `json:"requested_at"`
	RequestURL  string `json:"request_url"`
	OIDCContext any    `json:"oidc_context"`
}

type hydraAcceptLoginResponse struct {
	RedirectTo string `json:"redirect_to"`
}

// LoginGet handles GET /hydra/login — Hydra redirects here with ?login_challenge=...
func (h *HydraLoginHandler) LoginGet(c *gin.Context) {
	challenge := c.Query("login_challenge")
	if challenge == "" {
		c.String(http.StatusBadRequest, "missing login_challenge")
		return
	}

	// Fetch login request from Hydra admin.
	loginReq, err := h.getLoginRequest(challenge)
	if err != nil {
		log.WithError(err).Error("failed to get Hydra login request")
		c.String(http.StatusInternalServerError, "failed to get login request")
		return
	}

	// If Hydra says we can skip (user already authenticated), auto-accept.
	if loginReq.Skip {
		redirectTo, err := h.acceptLogin(challenge, loginReq.Subject, true)
		if err != nil {
			log.WithError(err).Error("failed to accept Hydra login")
			c.String(http.StatusInternalServerError, "failed to accept login")
			return
		}
		c.Redirect(http.StatusFound, redirectTo)
		return
	}

	// Check if any internal users exist — if not, show signup form.
	users, err := h.store.Users.ListInternalUsers(context.Background())
	if err != nil {
		log.WithError(err).Error("failed to list users")
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if len(users) == 0 {
		c.String(http.StatusOK, signupPage(challenge))
	} else {
		c.String(http.StatusOK, loginPage(challenge))
	}
}

// LoginPost handles POST /hydra/login — form submission with email + password + challenge,
// or TOTP verification via action=totp.
func (h *HydraLoginHandler) LoginPost(c *gin.Context) {
	action := c.PostForm("action") // "login", "signup", or "totp"

	// Handle TOTP verification step.
	if action == "totp" {
		h.handleTOTPVerification(c)
		return
	}

	challenge := c.PostForm("login_challenge")
	email := c.PostForm("email")
	password := c.PostForm("password")

	if challenge == "" || email == "" || password == "" {
		c.String(http.StatusBadRequest, "missing login_challenge, email, or password")
		return
	}

	ip := c.ClientIP()
	if h.limiter.blocked(ip) {
		log.WithField("ip", ip).Warn("login rate limited")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusTooManyRequests, errorPage("Too many failed login attempts. Please try again later."))
		return
	}

	ctx := context.Background()
	identity := "internal:" + email

	if action == "signup" {
		// Only allow signup if no internal users exist yet.
		users, err := h.store.Users.ListInternalUsers(ctx)
		if err != nil {
			log.WithError(err).Error("failed to list users")
			c.String(http.StatusInternalServerError, "internal error")
			return
		}
		if len(users) > 0 {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusForbidden, errorPage("Sign up is no longer available. An administrator must add your account."))
			return
		}

		// Hash the password.
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			log.WithError(err).Error("failed to hash password")
			c.String(http.StatusInternalServerError, "internal error")
			return
		}

		firstName := c.PostForm("first_name")
		lastName := c.PostForm("last_name")

		// Create the first user as super admin.
		entry := storeusers.UserEntry{
			Internal:   true,
			Email:      email,
			FirstName:  firstName,
			LastName:   lastName,
			SuperAdmin: true,
			Roles:      []string{},
		}
		if err := h.store.Users.UpsertInternalUser(ctx, entry); err != nil {
			log.WithError(err).Error("failed to create first user")
			c.String(http.StatusInternalServerError, "failed to create user")
			return
		}

		// Store the password hash.
		if err := h.store.Users.SetPasswordHash(ctx, email, string(hash)); err != nil {
			log.WithError(err).Error("failed to store password hash")
			c.String(http.StatusInternalServerError, "internal error")
			return
		}

		log.WithField("identity", identity).Info("first user created via Hydra signup")
	} else {
		// Login — verify user exists.
		user, err := h.store.Users.GetInternalUser(ctx, email)
		if err != nil {
			log.WithError(err).Error("failed to look up user")
			c.String(http.StatusInternalServerError, "internal error")
			return
		}
		if user == nil {
			h.limiter.record(ip)
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusForbidden, errorPage("Invalid email or password."))
			return
		}

		if user.Suspended {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusForbidden, errorPage("Your account has been suspended. Please contact an administrator."))
			return
		}

		// Verify password.
		storedHash, err := h.store.Users.GetPasswordHash(ctx, email)
		if err != nil {
			log.WithError(err).Error("failed to get password hash")
			c.String(http.StatusInternalServerError, "internal error")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)); err != nil {
			h.limiter.record(ip)
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusForbidden, errorPage("Invalid email or password."))
			return
		}

		// Password verified — check if TOTP is enabled.
		if user.TOTPEnabled {
			pendingToken := h.createPendingToken(email, challenge)
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, totpPage(challenge, pendingToken, ""))
			return
		}
	}

	redirectTo, err := h.acceptLogin(challenge, identity, false)
	if err != nil {
		log.WithError(err).Error("failed to accept Hydra login")
		c.String(http.StatusInternalServerError, "failed to accept login")
		return
	}

	c.Redirect(http.StatusFound, redirectTo)
}

// handleTOTPVerification handles the TOTP code verification step of the login flow.
func (h *HydraLoginHandler) handleTOTPVerification(c *gin.Context) {
	pendingToken := c.PostForm("pending_token")
	code := c.PostForm("totp_code")

	if pendingToken == "" || code == "" {
		c.String(http.StatusBadRequest, "missing pending_token or totp_code")
		return
	}

	ip := c.ClientIP()
	if h.limiter.blocked(ip) {
		log.WithField("ip", ip).Warn("TOTP rate limited")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusTooManyRequests, errorPage("Too many failed attempts. Please try again later."))
		return
	}

	email, challenge, err := h.validatePendingToken(pendingToken)
	if err != nil {
		log.WithError(err).Warn("invalid pending token")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusForbidden, errorPage("Session expired. Please log in again."))
		return
	}

	ctx := context.Background()

	// Get the encrypted TOTP secret.
	encryptedSecret, enabled, err := h.store.Users.GetTOTPSecret(ctx, email)
	if err != nil || !enabled || encryptedSecret == "" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusForbidden, errorPage("TOTP is not configured. Please log in again."))
		return
	}

	secret, err := h.store.Decrypt(encryptedSecret)
	if err != nil {
		log.WithError(err).Error("failed to decrypt TOTP secret")
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	// Try TOTP code first.
	if totp.Validate(code, secret) {
		identity := "internal:" + email
		redirectTo, err := h.acceptLogin(challenge, identity, false)
		if err != nil {
			log.WithError(err).Error("failed to accept Hydra login after TOTP")
			c.String(http.StatusInternalServerError, "failed to accept login")
			return
		}
		c.Redirect(http.StatusFound, redirectTo)
		return
	}

	// Try recovery code.
	codes, err := h.store.Users.GetTOTPRecoveryCodes(ctx, email)
	if err != nil {
		log.WithError(err).Error("failed to get recovery codes")
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	for i, hashedCode := range codes {
		if bcrypt.CompareHashAndPassword([]byte(hashedCode), []byte(code)) == nil {
			// Recovery code matched — consume it.
			remaining := append(codes[:i], codes[i+1:]...)
			if err := h.store.Users.UpdateTOTPRecoveryCodes(ctx, email, remaining); err != nil {
				log.WithError(err).Error("failed to consume recovery code")
			}

			identity := "internal:" + email
			redirectTo, err := h.acceptLogin(challenge, identity, false)
			if err != nil {
				log.WithError(err).Error("failed to accept Hydra login after recovery code")
				c.String(http.StatusInternalServerError, "failed to accept login")
				return
			}
			c.Redirect(http.StatusFound, redirectTo)
			return
		}
	}

	// Invalid code.
	h.limiter.record(ip)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, totpPage(challenge, pendingToken, "Invalid code. Please try again."))
}

func (h *HydraLoginHandler) getLoginRequest(challenge string) (*hydraLoginRequest, error) {
	req, err := http.NewRequest(http.MethodGet, h.adminURL+"/admin/oauth2/auth/requests/login?login_challenge="+challenge, nil)
	if err != nil {
		return nil, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result hydraLoginRequest
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (h *HydraLoginHandler) acceptLogin(challenge, subject string, remember bool) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"subject":      subject,
		"remember":     remember,
		"remember_for": 3600,
	})

	req, err := http.NewRequest(http.MethodPut,
		h.adminURL+"/admin/oauth2/auth/requests/login/accept?login_challenge="+challenge,
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	var result hydraAcceptLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.RedirectTo, nil
}

// --- Consent ---

type hydraConsentRequest struct {
	Challenge               string   `json:"challenge"`
	Skip                    bool     `json:"skip"`
	Subject                 string   `json:"subject"`
	RequestedScope          []string `json:"requested_scope"`
	RequestedAccessTokenAud []string `json:"requested_access_token_audience"`
}

type hydraAcceptConsentResponse struct {
	RedirectTo string `json:"redirect_to"`
}

// Consent handles GET /hydra/consent — auto-accepts all requested scopes.
func (h *HydraLoginHandler) Consent(c *gin.Context) {
	challenge := c.Query("consent_challenge")
	if challenge == "" {
		c.String(http.StatusBadRequest, "missing consent_challenge")
		return
	}

	// Fetch consent request from Hydra admin.
	consentReq, err := h.getConsentRequest(challenge)
	if err != nil {
		log.WithError(err).Error("failed to get Hydra consent request")
		c.String(http.StatusInternalServerError, "failed to get consent request")
		return
	}

	// Auto-accept: grant all requested scopes (first-party app).
	redirectTo, err := h.acceptConsent(challenge, consentReq)
	if err != nil {
		log.WithError(err).Error("failed to accept Hydra consent")
		c.String(http.StatusInternalServerError, "failed to accept consent")
		return
	}

	c.Redirect(http.StatusFound, redirectTo)
}

func (h *HydraLoginHandler) getConsentRequest(challenge string) (*hydraConsentRequest, error) {
	req, err := http.NewRequest(http.MethodGet, h.adminURL+"/admin/oauth2/auth/requests/consent?consent_challenge="+challenge, nil)
	if err != nil {
		return nil, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result hydraConsentRequest
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (h *HydraLoginHandler) acceptConsent(challenge string, consentReq *hydraConsentRequest) (string, error) {
	// Extract email from identity (strip "internal:" prefix if present).
	email := consentReq.Subject
	if len(email) > 9 && email[:9] == "internal:" {
		email = email[9:]
	}

	// Look up user to include real name in the ID token.
	name := email
	if user, err := h.store.Users.GetInternalUser(context.Background(), email); err == nil && user != nil {
		parts := []string{}
		if user.FirstName != "" {
			parts = append(parts, user.FirstName)
		}
		if user.LastName != "" {
			parts = append(parts, user.LastName)
		}
		if len(parts) > 0 {
			name = strings.Join(parts, " ")
		}
	}

	body, _ := json.Marshal(map[string]any{
		"grant_scope":                 consentReq.RequestedScope,
		"grant_access_token_audience": consentReq.RequestedAccessTokenAud,
		"remember":                    true,
		"remember_for":                3600,
		"session": map[string]any{
			"id_token": map[string]any{
				"email": email,
				"name":  name,
			},
		},
	})

	req, err := http.NewRequest(http.MethodPut,
		h.adminURL+"/admin/oauth2/auth/requests/consent/accept?consent_challenge="+challenge,
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	var result hydraAcceptConsentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.RedirectTo, nil
}

// --- HTML pages ---

const pageStyle = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f5f5f5; display: flex; justify-content: center; align-items: center; min-height: 100vh; }
  .card { background: #fff; border-radius: 8px; box-shadow: 0 2px 8px rgba(0,0,0,0.1); padding: 2rem; width: 100%; max-width: 400px; }
  h1 { font-size: 1.5rem; margin-bottom: 0.5rem; text-align: center; color: #333; }
  p { font-size: 0.875rem; color: #666; text-align: center; margin-bottom: 1.5rem; }
  label { display: block; font-size: 0.875rem; color: #555; margin-bottom: 0.25rem; }
  input[type="email"], input[type="text"], input[type="password"] { width: 100%; padding: 0.625rem; border: 1px solid #ddd; border-radius: 4px; font-size: 1rem; margin-bottom: 1rem; }
  input:focus { outline: none; border-color: #4a90d9; box-shadow: 0 0 0 2px rgba(74,144,217,0.2); }
  button { width: 100%; padding: 0.625rem; background: #4a90d9; color: #fff; border: none; border-radius: 4px; font-size: 1rem; cursor: pointer; }
  button:hover { background: #3a7bc8; }
  .error { color: #c0392b; text-align: center; }
`

func signupPage(challenge string) string {
	return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Create Admin Account</title>
<style>` + pageStyle + `</style>
</head>
<body>
<div class="card">
  <h1>Create Admin Account</h1>
  <p>No users exist yet. Create the first administrator account.</p>
  <form method="POST">
    <input type="hidden" name="login_challenge" value="` + challenge + `">
    <input type="hidden" name="action" value="signup">
    <label for="first_name">First Name</label>
    <input type="text" id="first_name" name="first_name" placeholder="First Name" required autofocus>
    <label for="last_name">Last Name</label>
    <input type="text" id="last_name" name="last_name" placeholder="Last Name" required>
    <label for="email">Email</label>
    <input type="email" id="email" name="email" placeholder="you@example.com" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" placeholder="Choose a password" minlength="8" required>
    <button type="submit">Create Account</button>
  </form>
</div>
</body>
</html>`
}

func loginPage(challenge string) string {
	return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Admont-AI Sign In</title>
<style>` + pageStyle + `</style>
</head>
<body>
<div class="card">
  <h1>Admont-AI Sign In</h1>
  <form method="POST">
    <input type="hidden" name="login_challenge" value="` + challenge + `">
    <input type="hidden" name="action" value="login">
    <label for="email">Email</label>
    <input type="email" id="email" name="email" placeholder="you@example.com" required autofocus>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" required>
    <button type="submit">Sign In</button>
  </form>
</div>
</body>
</html>`
}

func totpPage(challenge, pendingToken, errMsg string) string {
	errorHTML := ""
	if errMsg != "" {
		errorHTML = `<p class="error" style="margin-bottom: 1rem;">` + errMsg + `</p>`
	}
	return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Admont-AI 2FA</title>
<style>` + pageStyle + `
  .otp-inputs { display: flex; gap: 0.5rem; justify-content: center; margin-bottom: 1.5rem; }
  .otp-inputs input {
    width: 2.25rem; height: 2.75rem; text-align: center; font-size: 1.25rem; font-weight: 600;
    border: 1px solid #ddd; border-radius: 6px; padding: 0; margin: 0;
    -moz-appearance: textfield;
  }
  .otp-inputs input::-webkit-outer-spin-button,
  .otp-inputs input::-webkit-inner-spin-button { -webkit-appearance: none; margin: 0; }
  .otp-inputs input:focus { border-color: #4a90d9; box-shadow: 0 0 0 2px rgba(74,144,217,0.2); outline: none; }
  .toggle-link { display: block; text-align: center; margin-top: 1rem; font-size: 0.8125rem; color: #4a90d9; cursor: pointer; background: none; border: none; width: 100%; }
  .toggle-link:hover { text-decoration: underline; }
  .recovery-section { display: none; }
  .recovery-section.active { display: block; }
  .otp-section.hidden { display: none; }
</style>
</head>
<body>
<div class="card">
  <h1>Admont-AI 2FA</h1>
  <p id="otp-description">Enter the 6-digit code from your authenticator app.</p>
  ` + errorHTML + `
  <form id="totpForm" method="POST">
    <input type="hidden" name="login_challenge" value="` + challenge + `">
    <input type="hidden" name="action" value="totp">
    <input type="hidden" name="pending_token" value="` + pendingToken + `">
    <input type="hidden" id="totp_code" name="totp_code" value="">

    <div id="otpSection" class="otp-section">
      <div class="otp-inputs">
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="0" autocomplete="one-time-code" autofocus>
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="1">
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="2">
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="3">
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="4">
        <input type="text" inputmode="numeric" maxlength="1" class="otp-digit" data-index="5">
      </div>
    </div>

    <div id="recoverySection" class="recovery-section">
      <label for="recoveryInput">Recovery code</label>
      <input type="text" id="recoveryInput" placeholder="e.g. a1b2c3d4e5" autocomplete="off">
      <button type="button" id="recoverySubmit" onclick="submitRecovery()" style="margin-top: 0.25rem;">Verify</button>
    </div>

    <button type="button" id="toggleLink" class="toggle-link" onclick="toggleRecovery()">Use a recovery code instead</button>
  </form>
</div>
<script>
(function() {
  var digits = document.querySelectorAll('.otp-digit');
  var hidden = document.getElementById('totp_code');
  var form = document.getElementById('totpForm');

  function collectOTP() {
    var code = '';
    digits.forEach(function(d) { code += d.value; });
    return code;
  }

  digits.forEach(function(input, idx) {
    input.addEventListener('input', function(e) {
      var val = this.value.replace(/\D/g, '');
      this.value = val.slice(0, 1);
      if (val && idx < 5) {
        digits[idx + 1].focus();
      }
      var code = collectOTP();
      if (code.length === 6) {
        hidden.value = code;
        form.submit();
      }
    });

    input.addEventListener('keydown', function(e) {
      if (e.key === 'Backspace' && !this.value && idx > 0) {
        digits[idx - 1].focus();
        digits[idx - 1].value = '';
      }
    });

    input.addEventListener('paste', function(e) {
      e.preventDefault();
      var text = (e.clipboardData || window.clipboardData).getData('text').replace(/\D/g, '').slice(0, 6);
      for (var i = 0; i < text.length && (idx + i) < 6; i++) {
        digits[idx + i].value = text[i];
      }
      if (text.length > 0) {
        var last = Math.min(idx + text.length, 5);
        digits[last].focus();
      }
      var code = collectOTP();
      if (code.length === 6) {
        hidden.value = code;
        form.submit();
      }
    });
  });
})();

function toggleRecovery() {
  var otpSection = document.getElementById('otpSection');
  var recoverySection = document.getElementById('recoverySection');
  var toggleLink = document.getElementById('toggleLink');
  var description = document.getElementById('otp-description');
  var isRecovery = recoverySection.classList.contains('active');

  if (isRecovery) {
    recoverySection.classList.remove('active');
    otpSection.classList.remove('hidden');
    toggleLink.textContent = 'Use a recovery code instead';
    description.textContent = 'Enter the 6-digit code from your authenticator app.';
    document.querySelector('.otp-digit').focus();
  } else {
    otpSection.classList.add('hidden');
    recoverySection.classList.add('active');
    toggleLink.textContent = 'Use authenticator code instead';
    description.textContent = 'Enter one of your recovery codes.';
    document.getElementById('recoveryInput').focus();
  }
}

function submitRecovery() {
  var code = document.getElementById('recoveryInput').value.trim();
  if (!code) return;
  document.getElementById('totp_code').value = code;
  document.getElementById('totpForm').submit();
}

document.getElementById('recoveryInput').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') { e.preventDefault(); submitRecovery(); }
});
</script>
</body>
</html>`
}

func errorPage(message string) string {
	return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Error</title>
<style>` + pageStyle + `</style>
</head>
<body>
<div class="card">
  <h1 class="error">Error</h1>
  <p>` + message + `</p>
</div>
</body>
</html>`
}

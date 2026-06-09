package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/store"
	storeusers "github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors returned by the Authenticator. Callers should map these to
// user-facing messages without leaking which condition occurred (to avoid user
// enumeration), except where noted.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountSuspended   = errors.New("account suspended")
	ErrRateLimited        = errors.New("too many attempts")
	ErrSignupClosed       = errors.New("signup is closed")
	ErrWeakPassword       = errors.New("password too short")
	ErrInvalidTOTP        = errors.New("invalid code")
	ErrPendingToken       = errors.New("invalid or expired session")
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

// Authenticator performs transport-agnostic internal-user authentication
// (password, TOTP, recovery codes, first-user signup). It is shared by the
// web JSON login endpoints and the MCP browser login flow.
type Authenticator struct {
	store      *store.Store
	limiter    *loginRateLimiter
	signingKey []byte
	signupMu   sync.Mutex // serializes first-user signup
}

// NewAuthenticator creates an Authenticator. signingKey signs pending TOTP tokens.
func NewAuthenticator(st *store.Store, maxFailed, intervalMins int, signingKey []byte) *Authenticator {
	return &Authenticator{
		store:      st,
		limiter:    newLoginRateLimiter(maxFailed, time.Duration(intervalMins)*time.Minute),
		signingKey: signingKey,
	}
}

// Blocked reports whether the IP has exceeded the failed-attempt limit.
func (a *Authenticator) Blocked(ip string) bool { return a.limiter.blocked(ip) }

// Record registers a failed attempt for the IP.
func (a *Authenticator) Record(ip string) { a.limiter.record(ip) }

// VerifyPassword checks username/email + password. Looks up by username first,
// then falls back to email. On success returns the user; on failure returns a
// sentinel error and records a rate-limit strike.
func (a *Authenticator) VerifyPassword(ctx context.Context, ip, username, password string) (*storeusers.UserEntry, error) {
	user, err := a.store.Users.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	if user == nil {
		user, err = a.store.Users.GetInternalUser(ctx, username)
		if err != nil {
			return nil, fmt.Errorf("looking up user: %w", err)
		}
	}
	if user == nil {
		a.Record(ip)
		return nil, ErrInvalidCredentials
	}
	if user.Suspended {
		return nil, ErrAccountSuspended
	}
	storedHash, err := a.store.Users.GetPasswordHash(ctx, user.Email)
	if err != nil {
		return nil, fmt.Errorf("getting password hash: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) != nil {
		a.Record(ip)
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// VerifyTOTP validates a TOTP code or one-time recovery code for the user.
// A matched recovery code is consumed. Records a rate-limit strike on failure.
func (a *Authenticator) VerifyTOTP(ctx context.Context, ip, email, code string) error {
	encryptedSecret, enabled, err := a.store.Users.GetTOTPSecret(ctx, email)
	if err != nil || !enabled || encryptedSecret == "" {
		return ErrPendingToken
	}
	secret, err := a.store.Decrypt(encryptedSecret)
	if err != nil {
		return fmt.Errorf("decrypting TOTP secret: %w", err)
	}
	if totp.Validate(code, secret) {
		return nil
	}

	// Fall back to recovery codes (bcrypt-hashed, single-use).
	codes, err := a.store.Users.GetTOTPRecoveryCodes(ctx, email)
	if err != nil {
		return fmt.Errorf("getting recovery codes: %w", err)
	}
	for i, hashedCode := range codes {
		if bcrypt.CompareHashAndPassword([]byte(hashedCode), []byte(code)) == nil {
			remaining := append(codes[:i:i], codes[i+1:]...)
			if err := a.store.Users.UpdateTOTPRecoveryCodes(ctx, email, remaining); err != nil {
				return fmt.Errorf("consuming recovery code: %w", err)
			}
			return nil
		}
	}

	a.Record(ip)
	return ErrInvalidTOTP
}

// Signup creates the first internal user (super admin). It is only permitted
// when no internal users exist yet, and is serialized to prevent a race
// creating two super admins. The username is used as login identifier and also
// stored as the email for backward compatibility with identity strings.
func (a *Authenticator) Signup(ctx context.Context, username, password, firstName, lastName string) error {
	if len(password) < 8 {
		return ErrWeakPassword
	}
	a.signupMu.Lock()
	defer a.signupMu.Unlock()

	users, err := a.store.Users.ListInternalUsers(ctx)
	if err != nil {
		return fmt.Errorf("listing users: %w", err)
	}
	if len(users) > 0 {
		return ErrSignupClosed
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	entry := storeusers.UserEntry{
		Internal:   true,
		Username:   username,
		Email:      username,
		FirstName:  firstName,
		LastName:   lastName,
		SuperAdmin: true,
		Roles:      []string{},
	}
	if err := a.store.Users.UpsertInternalUser(ctx, entry); err != nil {
		return fmt.Errorf("creating first user: %w", err)
	}
	if err := a.store.Users.SetPasswordHash(ctx, username, string(hash)); err != nil {
		return fmt.Errorf("storing password hash: %w", err)
	}
	return nil
}

// DisplayName returns a human-friendly name for the internal user, or the email.
func (a *Authenticator) DisplayName(ctx context.Context, email string) string {
	u, err := a.store.Users.GetInternalUser(ctx, email)
	if err != nil || u == nil {
		return email
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		return email
	}
	return name
}

// SignupOpen reports whether first-user signup is still available.
func (a *Authenticator) SignupOpen(ctx context.Context) bool {
	users, err := a.store.Users.ListInternalUsers(ctx)
	return err == nil && len(users) == 0
}

const pendingAuthTokenTTL = 5 * time.Minute

// CreatePendingToken issues an HMAC-signed, time-limited token binding a
// half-authenticated session (password verified, awaiting TOTP) to an email.
// Format: email|expiry|hexsig
func (a *Authenticator) CreatePendingToken(email string) string {
	expiry := strconv.FormatInt(time.Now().Add(pendingAuthTokenTTL).Unix(), 10)
	data := email + "|" + expiry
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write([]byte(data))
	return data + "|" + hex.EncodeToString(mac.Sum(nil))
}

// ValidatePendingToken verifies a pending token and returns the bound email.
func (a *Authenticator) ValidatePendingToken(token string) (string, error) {
	parts := strings.SplitN(token, "|", 3)
	if len(parts) != 3 {
		return "", ErrPendingToken
	}
	email, expiryStr, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write([]byte(email + "|" + expiryStr))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return "", ErrPendingToken
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return "", ErrPendingToken
	}
	return email, nil
}

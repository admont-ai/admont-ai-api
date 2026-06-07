package auth

import (
	"errors"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Identity string `json:"identity,omitempty"`
	jwt.RegisteredClaims
}

type RefreshClaims struct {
	Email    string `json:"email"`
	Provider string `json:"provider,omitempty"`
	Identity string `json:"identity,omitempty"`
	jwt.RegisteredClaims
}

type JWTService struct {
	secret            []byte
	expiration        time.Duration
	refreshExpiration time.Duration

	// invalidatedBefore tracks, per identity, a cutoff time before which all
	// tokens are rejected. Used to invalidate sessions on password change.
	// In-memory: a process restart clears it, but tokens expire within
	// expiration/refreshExpiration anyway.
	mu                sync.RWMutex
	invalidatedBefore map[string]time.Time
}

func NewJWTService(secret string, expiration time.Duration) *JWTService {
	return &JWTService{
		secret:            []byte(secret),
		expiration:        expiration,
		refreshExpiration: 7 * 24 * time.Hour,
		invalidatedBefore: make(map[string]time.Time),
	}
}

// InvalidateSessions rejects all tokens for the given identity issued before now.
// Call this when a user's password changes or credentials are otherwise revoked.
func (s *JWTService) InvalidateSessions(identity string) {
	if identity == "" {
		return
	}
	s.mu.Lock()
	s.invalidatedBefore[identity] = time.Now()
	s.mu.Unlock()
}

// isInvalidated reports whether a token for identity issued at issuedAt has been
// invalidated by a later InvalidateSessions call.
func (s *JWTService) isInvalidated(identity string, issuedAt *jwt.NumericDate) bool {
	if identity == "" || issuedAt == nil {
		return false
	}
	s.mu.RLock()
	cutoff, ok := s.invalidatedBefore[identity]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	// JWT "iat" has one-second resolution, so reject any token issued in or
	// before the cutoff second. A token minted in a later second is accepted.
	return !issuedAt.Time.After(cutoff.Truncate(time.Second))
}

func (s *JWTService) GenerateToken(email, name, subject, provider string) (string, error) {
	identity := email
	if provider != "" {
		identity = provider + ":" + email
	}
	now := time.Now()
	claims := Claims{
		Email:    email,
		Name:     name,
		Provider: provider,
		Identity: identity,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.expiration)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

func (s *JWTService) GenerateRefreshToken(email, provider string) (string, error) {
	identity := email
	if provider != "" {
		identity = provider + ":" + email
	}
	now := time.Now()
	claims := RefreshClaims{
		Email:    email,
		Provider: provider,
		Identity: identity,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "refresh",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiration)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

func (s *JWTService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	if s.isInvalidated(claims.Identity, claims.IssuedAt) {
		return nil, errors.New("token invalidated")
	}

	return claims, nil
}

func (s *JWTService) ValidateRefreshToken(tokenString string) (*RefreshClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &RefreshClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*RefreshClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid refresh token")
	}
	if claims.Subject != "refresh" {
		return nil, errors.New("not a refresh token")
	}
	if s.isInvalidated(claims.Identity, claims.IssuedAt) {
		return nil, errors.New("token invalidated")
	}

	return claims, nil
}

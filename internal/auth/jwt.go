package auth

import (
	"errors"
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

type JWTService struct {
	secret     []byte
	expiration time.Duration
}

func NewJWTService(secret string, expiration time.Duration) *JWTService {
	return &JWTService{
		secret:     []byte(secret),
		expiration: expiration,
	}
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

	return claims, nil
}

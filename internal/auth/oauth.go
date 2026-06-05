package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
)

// OAuthService is a thin wrapper around an oauth2.Config for token exchange.
// State management has been moved to Registry.
type OAuthService struct {
	config *oauth2.Config
}

func NewOAuthService(oauthConfig *oauth2.Config) *OAuthService {
	return &OAuthService{config: oauthConfig}
}

func (s *OAuthService) Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	token, err := s.config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}
	return token, nil
}

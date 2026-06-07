package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// UserInfo holds the user information extracted after OAuth exchange.
type UserInfo struct {
	Email    string
	Name     string
	Subject  string
	Provider string
}

type pkceState struct {
	CodeVerifier     string
	FrontendRedirect string
	ProviderName     string
	CreatedAt        time.Time
}

// Registry manages multiple OAuth providers with shared PKCE state.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*ProviderEntry
	states    sync.Map
	ttl       time.Duration
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	r := &Registry{
		providers: make(map[string]*ProviderEntry),
		ttl:       10 * time.Minute,
	}
	go r.cleanupLoop()
	return r
}

// Register adds a provider to the registry.
func (r *Registry) Register(entry *ProviderEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[entry.Name] = entry
}

// Unregister removes a provider from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}

// Names returns a sorted list of registered provider names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// GenerateAuthURL generates an OAuth authorization URL for the given provider.
func (r *Registry) GenerateAuthURL(providerName, frontendRedirect string) (authURL, state string, err error) {
	r.mu.RLock()
	entry, ok := r.providers[providerName]
	r.mu.RUnlock()
	if !ok {
		return "", "", fmt.Errorf("unknown provider: %q", providerName)
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", fmt.Errorf("generating state: %w", err)
	}
	state = base64.RawURLEncoding.EncodeToString(stateBytes)

	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("generating verifier: %w", err)
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	r.states.Store(state, &pkceState{
		CodeVerifier:     codeVerifier,
		FrontendRedirect: frontendRedirect,
		ProviderName:     providerName,
		CreatedAt:        time.Now(),
	})

	authURL = entry.OAuthConfig.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	return authURL, state, nil
}

// ExchangeAndValidate exchanges the authorization code and fetches user info.
func (r *Registry) ExchangeAndValidate(ctx context.Context, state, code string) (*UserInfo, string, error) {
	val, ok := r.states.LoadAndDelete(state)
	if !ok {
		return nil, "", errors.New("invalid or expired state")
	}
	ps := val.(*pkceState)

	if time.Since(ps.CreatedAt) > r.ttl {
		return nil, "", errors.New("state expired")
	}

	r.mu.RLock()
	entry, ok := r.providers[ps.ProviderName]
	r.mu.RUnlock()
	if !ok {
		return nil, "", fmt.Errorf("provider %q not found", ps.ProviderName)
	}

	token, err := entry.OAuthConfig.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", ps.CodeVerifier))
	if err != nil {
		return nil, "", fmt.Errorf("exchanging code: %w", err)
	}

	sessionData := map[string]string{
		"AuthURL":     entry.OAuthConfig.Endpoint.AuthURL,
		"AccessToken": token.AccessToken,
	}
	if idToken, ok := token.Extra("id_token").(string); ok && idToken != "" {
		sessionData["IDToken"] = idToken
	}
	sessionJSON, _ := json.Marshal(sessionData)
	sess, err := entry.GothProvider.UnmarshalSession(string(sessionJSON))
	if err != nil {
		return nil, "", fmt.Errorf("creating goth session: %w", err)
	}

	gothUser, err := entry.GothProvider.FetchUser(sess)
	if err != nil {
		return nil, "", fmt.Errorf("fetching user info: %w", err)
	}

	userInfo := &UserInfo{
		Email:    gothUser.Email,
		Name:     gothUser.Name,
		Subject:  gothUser.UserID,
		Provider: ps.ProviderName,
	}

	return userInfo, ps.FrontendRedirect, nil
}

// LookupState retrieves and removes the PKCE state entry for the given state string.
// Returns the state data if found and not expired, or an error otherwise.
func (r *Registry) LookupState(state string) (*pkceState, error) {
	val, ok := r.states.LoadAndDelete(state)
	if !ok {
		return nil, errors.New("invalid or expired state")
	}
	ps := val.(*pkceState)
	if time.Since(ps.CreatedAt) > r.ttl {
		return nil, errors.New("state expired")
	}
	return ps, nil
}

// ExchangeCode exchanges the authorization code for tokens using the provider from the state.
func (r *Registry) ExchangeCode(ctx context.Context, providerName, code, codeVerifier string) (*oauth2.Token, error) {
	r.mu.RLock()
	entry, ok := r.providers[providerName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}
	token, err := entry.OAuthConfig.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}
	return token, nil
}

// FetchUser fetches user info from the provider using the access token.
func (r *Registry) FetchUser(providerName, accessToken string) (*UserInfo, error) {
	r.mu.RLock()
	entry, ok := r.providers[providerName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	fetchSessionData := map[string]string{
		"AuthURL":     entry.OAuthConfig.Endpoint.AuthURL,
		"AccessToken": accessToken,
	}
	fetchSessionJSON, _ := json.Marshal(fetchSessionData)
	sess, err := entry.GothProvider.UnmarshalSession(string(fetchSessionJSON))
	if err != nil {
		return nil, fmt.Errorf("creating goth session: %w", err)
	}

	gothUser, err := entry.GothProvider.FetchUser(sess)
	if err != nil {
		return nil, fmt.Errorf("fetching user info: %w", err)
	}

	return &UserInfo{
		Email:    gothUser.Email,
		Name:     gothUser.Name,
		Subject:  gothUser.UserID,
		Provider: providerName,
	}, nil
}

// Provider returns the provider entry for the given name.
func (r *Registry) Provider(name string) (*ProviderEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.providers[name]
	return e, ok
}

func (r *Registry) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		r.states.Range(func(key, value any) bool {
			ps := value.(*pkceState)
			if now.Sub(ps.CreatedAt) > r.ttl {
				r.states.Delete(key)
			}
			return true
		})
	}
}

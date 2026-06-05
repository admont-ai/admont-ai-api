package auth

import (
	"testing"

	storeauth "github.com/christianfischer/md-wiki-server/internal/store/auth_provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRegistry(t *testing.T) (*Registry, *ProviderEntry) {
	t.Helper()
	reg := &Registry{
		providers: make(map[string]*ProviderEntry),
		ttl:       10 * 60 * 1e9, // 10 minutes
	}
	cfg := storeauth.AuthProvider{
		Name:         "google",
		ClientID:     "test-id",
		ClientSecret: "test-secret",
	}
	entry, err := NewProviderFromConfig(cfg, "http://localhost/callback")
	require.NoError(t, err)
	return reg, entry
}

func TestRegistry_RegisterAndNames(t *testing.T) {
	reg, entry := newTestRegistry(t)

	assert.Empty(t, reg.Names())

	reg.Register(entry)
	names := reg.Names()
	assert.Equal(t, 1, len(names))
	assert.Equal(t, "google", names[0])
}

func TestRegistry_Unregister(t *testing.T) {
	reg, entry := newTestRegistry(t)

	reg.Register(entry)
	assert.Equal(t, 1, len(reg.Names()))

	reg.Unregister("google")
	assert.Empty(t, reg.Names())
}

func TestRegistry_UnregisterNonExistent(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.Unregister("nonexistent")
	assert.Empty(t, reg.Names())
}

func TestRegistry_Provider(t *testing.T) {
	reg, entry := newTestRegistry(t)
	reg.Register(entry)

	got, ok := reg.Provider("google")
	assert.True(t, ok)
	assert.Equal(t, "google", got.Name)

	_, ok = reg.Provider("missing")
	assert.False(t, ok)
}

func TestRegistry_MultipleProviders(t *testing.T) {
	reg, googleEntry := newTestRegistry(t)
	reg.Register(googleEntry)

	ghCfg := storeauth.AuthProvider{
		Name:         "github",
		ClientID:     "gh-id",
		ClientSecret: "gh-secret",
	}
	ghEntry, err := NewProviderFromConfig(ghCfg, "http://localhost/callback")
	require.NoError(t, err)
	reg.Register(ghEntry)

	names := reg.Names()
	assert.Equal(t, 2, len(names))
	assert.Contains(t, names, "google")
	assert.Contains(t, names, "github")
}

func TestRegistry_GenerateAuthURL(t *testing.T) {
	reg, entry := newTestRegistry(t)
	reg.Register(entry)

	authURL, state, err := reg.GenerateAuthURL("google", "http://frontend/callback")
	require.NoError(t, err)
	assert.NotEmpty(t, authURL)
	assert.NotEmpty(t, state)
	assert.Contains(t, authURL, "code_challenge")
}

func TestRegistry_GenerateAuthURL_UnknownProvider(t *testing.T) {
	reg, _ := newTestRegistry(t)

	_, _, err := reg.GenerateAuthURL("nonexistent", "http://frontend/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestRegistry_LookupState_Invalid(t *testing.T) {
	reg, _ := newTestRegistry(t)

	_, err := reg.LookupState("nonexistent-state")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired state")
}

func TestRegistry_LookupState_Valid(t *testing.T) {
	reg, entry := newTestRegistry(t)
	reg.Register(entry)

	_, state, err := reg.GenerateAuthURL("google", "http://frontend")
	require.NoError(t, err)

	ps, err := reg.LookupState(state)
	require.NoError(t, err)
	assert.Equal(t, "google", ps.ProviderName)
	assert.Equal(t, "http://frontend", ps.FrontendRedirect)
	assert.NotEmpty(t, ps.CodeVerifier)
}

func TestRegistry_LookupState_ConsumedOnce(t *testing.T) {
	reg, entry := newTestRegistry(t)
	reg.Register(entry)

	_, state, err := reg.GenerateAuthURL("google", "http://frontend")
	require.NoError(t, err)

	_, err = reg.LookupState(state)
	require.NoError(t, err)

	_, err = reg.LookupState(state)
	assert.Error(t, err, "state should be consumed after first lookup")
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	reg, entry := newTestRegistry(t)
	reg.Register(entry)

	newCfg := storeauth.AuthProvider{
		Name:         "google",
		ClientID:     "new-id",
		ClientSecret: "new-secret",
	}
	newEntry, err := NewProviderFromConfig(newCfg, "http://localhost/callback2")
	require.NoError(t, err)
	reg.Register(newEntry)

	got, ok := reg.Provider("google")
	assert.True(t, ok)
	assert.Equal(t, "new-id", got.OAuthConfig.ClientID)
}

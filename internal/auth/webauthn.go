package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/christianfischer/md-wiki-server/internal/publicurl"
	"github.com/christianfischer/md-wiki-server/internal/store"
	storeusers "github.com/christianfischer/md-wiki-server/internal/store/users"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ErrWebAuthnDisabled is returned when passkey support is not configured.
var ErrWebAuthnDisabled = errors.New("passkeys are not enabled")

const webauthnSessionTTL = 5 * time.Minute

// WebAuthnManager wraps the go-webauthn relying party and the short-lived
// ceremony state (challenges) shared between the begin and finish steps.
//
// With a static configuration (NewWebAuthnManager) one relying party is used.
// With a dynamic one (NewDynamicWebAuthnManager) the relying party follows the
// public host of each request (RP ID = host, origin = scheme://host), so the
// API can serve UIs on several hosts. Passkeys are bound to the RP ID, so a
// passkey registered on one host only works on that host.
type WebAuthnManager struct {
	wa       *webauthn.WebAuthn // static relying party; nil when dynamic
	store    *store.Store
	sessions sync.Map // sessionID -> *webauthnCeremony

	displayName string
	publicURL   *publicurl.Resolver
	dynamic     sync.Map // origin -> *webauthn.WebAuthn
	dynamicN    atomic.Int32
}

// maxDynamicRelyingParties caps the per-host relying-party cache.
const maxDynamicRelyingParties = 64

type webauthnCeremony struct {
	data    *webauthn.SessionData
	email   string // set for registration ceremonies; empty for discoverable login
	expires time.Time
	wa      *webauthn.WebAuthn // relying party the ceremony was started with
}

// NewWebAuthnManager builds a relying party for the given RP ID / origins.
// Registration and login both require user verification, which is what lets a
// passkey stand in for both authentication factors (so TOTP is skipped).
func NewWebAuthnManager(st *store.Store, rpID, rpDisplayName string, origins []string) (*WebAuthnManager, error) {
	wa, err := newRelyingParty(rpID, rpDisplayName, origins)
	if err != nil {
		return nil, err
	}
	m := &WebAuthnManager{wa: wa, store: st, displayName: rpDisplayName}
	go m.cleanupLoop()
	return m, nil
}

// NewDynamicWebAuthnManager builds a manager whose relying party is derived
// from each request's public host via the resolver.
func NewDynamicWebAuthnManager(st *store.Store, rpDisplayName string, resolver *publicurl.Resolver) (*WebAuthnManager, error) {
	if resolver == nil {
		return nil, errors.New("webauthn: public URL resolver required")
	}
	m := &WebAuthnManager{store: st, displayName: rpDisplayName, publicURL: resolver}
	go m.cleanupLoop()
	return m, nil
}

func newRelyingParty(rpID, rpDisplayName string, origins []string) (*webauthn.WebAuthn, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configuring webauthn: %w", err)
	}
	return wa, nil
}

// relyingParty returns the relying party for the request.
func (m *WebAuthnManager) relyingParty(r *http.Request) (*webauthn.WebAuthn, error) {
	if m.wa != nil {
		return m.wa, nil
	}
	if r == nil {
		return nil, errors.New("webauthn: request required to derive relying party")
	}
	origin := m.publicURL.Origin(r)
	if v, ok := m.dynamic.Load(origin); ok {
		return v.(*webauthn.WebAuthn), nil
	}
	wa, err := newRelyingParty(m.publicURL.Host(r), m.displayName, []string{origin})
	if err != nil {
		return nil, err
	}
	if m.dynamicN.Load() < maxDynamicRelyingParties {
		if v, loaded := m.dynamic.LoadOrStore(origin, wa); loaded {
			return v.(*webauthn.WebAuthn), nil
		}
		m.dynamicN.Add(1)
	}
	return wa, nil
}

// --- webauthn.User adapter ---

type webauthnUser struct {
	id      int
	email   string
	name    string
	display string
	creds   []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(u.id))
	return b
}
func (u *webauthnUser) WebAuthnName() string                   { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string            { return u.display }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (m *WebAuthnManager) buildUser(ctx context.Context, entry *storeusers.UserEntry) (*webauthnUser, error) {
	stored, err := m.store.Users.ListWebAuthnCredentialsByUserID(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(stored))
	for _, c := range stored {
		creds = append(creds, toWebauthnCredential(c))
	}
	name := entry.Username
	if name == "" {
		name = entry.Email
	}
	display := strings.TrimSpace(entry.FirstName + " " + entry.LastName)
	if display == "" {
		display = name
	}
	return &webauthnUser{id: entry.ID, email: entry.Email, name: name, display: display, creds: creds}, nil
}

func toWebauthnCredential(c storeusers.WebAuthnCredential) webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, len(c.Transports))
	for i, t := range c.Transports {
		transports[i] = protocol.AuthenticatorTransport(t)
	}
	return webauthn.Credential{
		ID:              c.CredentialID,
		PublicKey:       c.PublicKey,
		AttestationType: c.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			BackupEligible: c.BackupEligible,
			BackupState:    c.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    c.AAGUID,
			SignCount: uint32(c.SignCount),
		},
	}
}

func credentialToStore(c *webauthn.Credential, name string) storeusers.WebAuthnCredential {
	transports := make([]string, len(c.Transport))
	for i, t := range c.Transport {
		transports[i] = string(t)
	}
	return storeusers.WebAuthnCredential{
		CredentialID:    c.ID,
		PublicKey:       c.PublicKey,
		AttestationType: c.AttestationType,
		Transports:      transports,
		AAGUID:          c.Authenticator.AAGUID,
		SignCount:       int64(c.Authenticator.SignCount),
		BackupEligible:  c.Flags.BackupEligible,
		BackupState:     c.Flags.BackupState,
		Name:            name,
	}
}

// --- ceremony session store ---

func (m *WebAuthnManager) storeCeremony(wa *webauthn.WebAuthn, data *webauthn.SessionData, email string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	m.sessions.Store(id, &webauthnCeremony{data: data, email: email, expires: time.Now().Add(webauthnSessionTTL), wa: wa})
	return id, nil
}

// takeCeremony looks up and removes a ceremony (single-use), enforcing the TTL.
func (m *WebAuthnManager) takeCeremony(id string) (*webauthnCeremony, bool) {
	v, ok := m.sessions.LoadAndDelete(id)
	if !ok {
		return nil, false
	}
	c := v.(*webauthnCeremony)
	if time.Now().After(c.expires) {
		return nil, false
	}
	return c, true
}

func (m *WebAuthnManager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		m.sessions.Range(func(key, value any) bool {
			if now.After(value.(*webauthnCeremony).expires) {
				m.sessions.Delete(key)
			}
			return true
		})
	}
}

// --- Registration (authenticated user adds a passkey) ---

// BeginRegistration starts a passkey registration for the logged-in user and
// returns the creation options plus an opaque ceremony session id.
func (m *WebAuthnManager) BeginRegistration(ctx context.Context, email string, r *http.Request) (*protocol.CredentialCreation, string, error) {
	wa, err := m.relyingParty(r)
	if err != nil {
		return nil, "", err
	}
	entry, err := m.store.Users.GetInternalUser(ctx, email)
	if err != nil {
		return nil, "", err
	}
	if entry == nil {
		return nil, "", errors.New("user not found")
	}
	user, err := m.buildUser(ctx, entry)
	if err != nil {
		return nil, "", err
	}
	creation, session, err := wa.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		return nil, "", fmt.Errorf("beginning registration: %w", err)
	}
	id, err := m.storeCeremony(wa, session, email)
	if err != nil {
		return nil, "", err
	}
	return creation, id, nil
}

// FinishRegistration verifies the attestation response and persists the new
// credential under the given name.
func (m *WebAuthnManager) FinishRegistration(ctx context.Context, sessionID, name string, r *http.Request) error {
	ceremony, ok := m.takeCeremony(sessionID)
	if !ok || ceremony.email == "" {
		return errors.New("registration session expired; please try again")
	}
	entry, err := m.store.Users.GetInternalUser(ctx, ceremony.email)
	if err != nil || entry == nil {
		return errors.New("user not found")
	}
	user, err := m.buildUser(ctx, entry)
	if err != nil {
		return err
	}
	credential, err := ceremony.wa.FinishRegistration(user, *ceremony.data, r)
	if err != nil {
		return fmt.Errorf("verifying registration: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		name = "Passkey"
	}
	return m.store.Users.AddWebAuthnCredential(ctx, ceremony.email, credentialToStore(credential, name))
}

// --- Discoverable (usernameless) login ---

// BeginLogin starts a discoverable passkey login and returns the assertion
// options plus an opaque ceremony session id.
func (m *WebAuthnManager) BeginLogin(r *http.Request) (*protocol.CredentialAssertion, string, error) {
	wa, err := m.relyingParty(r)
	if err != nil {
		return nil, "", err
	}
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", fmt.Errorf("beginning login: %w", err)
	}
	id, err := m.storeCeremony(wa, session, "")
	if err != nil {
		return nil, "", err
	}
	return assertion, id, nil
}

// FinishLogin verifies the assertion, persists the updated sign count, and
// returns the authenticated user's email.
func (m *WebAuthnManager) FinishLogin(ctx context.Context, sessionID string, r *http.Request) (string, error) {
	ceremony, ok := m.takeCeremony(sessionID)
	if !ok {
		return "", errors.New("login session expired; please try again")
	}

	handler := func(_, userHandle []byte) (webauthn.User, error) {
		if len(userHandle) != 8 {
			return nil, errors.New("invalid user handle")
		}
		id := int(binary.BigEndian.Uint64(userHandle))
		entry, err := m.store.Users.GetInternalUserByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, errors.New("user not found")
		}
		if entry.Suspended {
			return nil, ErrAccountSuspended
		}
		return m.buildUser(ctx, entry)
	}

	user, credential, err := ceremony.wa.FinishPasskeyLogin(handler, *ceremony.data, r)
	if err != nil {
		return "", fmt.Errorf("verifying login: %w", err)
	}
	wu, ok := user.(*webauthnUser)
	if !ok {
		return "", errors.New("internal error")
	}
	if err := m.store.Users.UpdateWebAuthnCredentialOnLogin(ctx, credential.ID,
		int64(credential.Authenticator.SignCount), credential.Flags.BackupState); err != nil {
		return "", err
	}
	return wu.email, nil
}

// ListCredentials returns the user's passkeys for the management UI.
func (m *WebAuthnManager) ListCredentials(ctx context.Context, email string) ([]storeusers.WebAuthnCredential, error) {
	return m.store.Users.ListWebAuthnCredentials(ctx, email)
}

// RenameCredential renames a passkey owned by the user.
func (m *WebAuthnManager) RenameCredential(ctx context.Context, email string, id int, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	return m.store.Users.RenameWebAuthnCredential(ctx, email, id, name)
}

// DeleteCredential removes a passkey owned by the user.
func (m *WebAuthnManager) DeleteCredential(ctx context.Context, email string, id int) error {
	return m.store.Users.DeleteWebAuthnCredential(ctx, email, id)
}

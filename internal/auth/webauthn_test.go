package auth

import (
	"encoding/binary"
	"testing"

	storeusers "github.com/christianfischer/md-wiki-server/internal/store/users"
)

func TestWebAuthnUserHandleRoundTrip(t *testing.T) {
	for _, id := range []int{1, 42, 65535, 1 << 20} {
		u := &webauthnUser{id: id}
		h := u.WebAuthnID()
		if len(h) != 8 {
			t.Fatalf("handle length = %d, want 8", len(h))
		}
		if got := int(binary.BigEndian.Uint64(h)); got != id {
			t.Errorf("decoded handle = %d, want %d", got, id)
		}
	}
}

func TestCredentialConversionRoundTrip(t *testing.T) {
	stored := storeusers.WebAuthnCredential{
		CredentialID:    []byte{1, 2, 3, 4},
		PublicKey:       []byte{9, 8, 7},
		AttestationType: "none",
		Transports:      []string{"internal", "hybrid"},
		AAGUID:          []byte{0xaa, 0xbb},
		SignCount:       7,
		BackupEligible:  true,
		BackupState:     true,
	}

	wc := toWebauthnCredential(stored)
	if string(wc.ID) != string(stored.CredentialID) || string(wc.PublicKey) != string(stored.PublicKey) {
		t.Fatal("id/public key not preserved")
	}
	if wc.Authenticator.SignCount != 7 || !wc.Flags.BackupEligible || !wc.Flags.BackupState {
		t.Fatal("authenticator/flags not preserved")
	}
	if len(wc.Transport) != 2 || string(wc.Transport[0]) != "internal" {
		t.Fatalf("transports not preserved: %v", wc.Transport)
	}

	back := credentialToStore(&wc, "My Key")
	if back.Name != "My Key" {
		t.Errorf("name = %q, want My Key", back.Name)
	}
	if back.SignCount != stored.SignCount || back.BackupEligible != stored.BackupEligible {
		t.Error("round-trip lost authenticator data")
	}
	if len(back.Transports) != 2 || back.Transports[1] != "hybrid" {
		t.Errorf("round-trip lost transports: %v", back.Transports)
	}
}

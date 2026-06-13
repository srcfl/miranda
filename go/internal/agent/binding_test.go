// go/internal/agent/binding_test.go
package agent

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// TestOwnerPubFromBinding proves the agent recovers the Noise-KK X25519 pin from
// the offer's wallet-signed binding (not by hex-decoding owner_id). A binding is
// accepted only when its wallet == owner_id and its signature verifies; the pinned
// key is binding.x25519. device is the owner's device id, not the agent's
// machine_id, so it is not checked.
func TestOwnerPubFromBinding(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 32)
	w, err := identity.DeriveWallet(secret)
	if err != nil {
		t.Fatalf("DeriveWallet: %v", err)
	}
	_, pub, err := identity.DeriveOwnerKey(secret)
	if err != nil {
		t.Fatalf("DeriveOwnerKey: %v", err)
	}
	x25519hex := hex.EncodeToString(pub)
	const device = "owner-laptop-1"
	const ts = int64(1_700_000_000)

	sb, err := w.SignBinding(device, x25519hex, ts)
	if err != nil {
		t.Fatalf("SignBinding: %v", err)
	}
	goodJSON, err := sb.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// A second, unrelated wallet — its address is a valid base58 owner_id that
	// does not match the binding's wallet.
	other, err := identity.DeriveWallet(bytes.Repeat([]byte{0x07}, 32))
	if err != nil {
		t.Fatalf("DeriveWallet(other): %v", err)
	}

	// Tamper one character of the base58 signature inside the rendered JSON.
	tampered := tamperSig(t, goodJSON, sb.Sig)

	cases := []struct {
		name        string
		bindingJSON string
		owner       string
		wantErr     bool
		wantPub     []byte
	}{
		{name: "good", bindingJSON: goodJSON, owner: w.Address, wantErr: false, wantPub: pub},
		{name: "wrong wallet", bindingJSON: goodJSON, owner: other.Address, wantErr: true},
		{name: "tampered signature", bindingJSON: tampered, owner: w.Address, wantErr: true},
		{name: "empty binding", bindingJSON: "", owner: w.Address, wantErr: true},
		{name: "malformed JSON", bindingJSON: "{not json", owner: w.Address, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ownerPubFromBinding(tc.bindingJSON, tc.owner)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got pub %x", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, tc.wantPub) {
				t.Fatalf("pin mismatch: got %x want %x", got, tc.wantPub)
			}
		})
	}
}

// ownerBinding mints a wallet-rooted owner identity for handleOffer tests: it
// derives the wallet (owner_id) and the X25519 transport keypair from a shared
// secret, then signs a binding authorizing that x25519 under the wallet for the
// given device id. Returns the Noise initiator static keypair, the base58 owner_id
// to register/pin, and the signed-binding JSON to attach to the offer.
func ownerBinding(t *testing.T, secret []byte, device string) (priv, pub []byte, ownerID, bindingJSON string) {
	t.Helper()
	w, err := identity.DeriveWallet(secret)
	if err != nil {
		t.Fatalf("DeriveWallet: %v", err)
	}
	priv, pub, err = identity.DeriveOwnerKey(secret)
	if err != nil {
		t.Fatalf("DeriveOwnerKey: %v", err)
	}
	sb, err := w.SignBinding(device, hex.EncodeToString(pub), 1_700_000_000)
	if err != nil {
		t.Fatalf("SignBinding: %v", err)
	}
	bindingJSON, err = sb.JSON()
	if err != nil {
		t.Fatalf("binding JSON: %v", err)
	}
	return priv, pub, w.Address, bindingJSON
}

// tamperSig flips one character of the signature substring inside the JSON so the
// record stays well-formed JSON but the signature no longer verifies.
func tamperSig(t *testing.T, jsonStr, sig string) string {
	t.Helper()
	if !strings.Contains(jsonStr, sig) {
		t.Fatalf("sig %q not found in JSON %q", sig, jsonStr)
	}
	b := []byte(sig)
	// base58 has no '0'; pick a char distinct from the original to guarantee a flip.
	if b[0] == '1' {
		b[0] = '2'
	} else {
		b[0] = '1'
	}
	return strings.Replace(jsonStr, sig, string(b), 1)
}

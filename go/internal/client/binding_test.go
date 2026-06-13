// go/internal/client/binding_test.go
package client

import (
	"regexp"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

var deviceIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// TestSetFromSecretCachesBinding asserts that rooting an identity in a secret
// also mints a stable device id and a cached wallet->x25519 binding that
// verifies and matches the identity's wallet/transport key/device.
func TestSetFromSecretCachesBinding(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	id := &Identity{}
	if err := id.SetFromSecret(secret); err != nil {
		t.Fatal(err)
	}

	if !deviceIDRe.MatchString(id.DeviceID) {
		t.Fatalf("device id %q does not match ^[0-9a-f]{16}$", id.DeviceID)
	}
	if id.BindingJSON == "" {
		t.Fatal("binding json was not cached")
	}

	sb, err := identity.ParseSignedBinding([]byte(id.BindingJSON))
	if err != nil {
		t.Fatalf("parse binding: %v", err)
	}
	if err := identity.VerifyBinding(sb); err != nil {
		t.Fatalf("verify binding: %v", err)
	}
	if sb.Wallet != id.WalletAddress {
		t.Fatalf("binding wallet %q != identity wallet %q", sb.Wallet, id.WalletAddress)
	}
	if sb.X25519 != id.OwnerPubHex {
		t.Fatalf("binding x25519 %q != owner pub %q", sb.X25519, id.OwnerPubHex)
	}
	if sb.Device != id.DeviceID {
		t.Fatalf("binding device %q != device id %q", sb.Device, id.DeviceID)
	}
}

// TestRekeyRotatesDeviceAndBinding asserts that Rekey mints a fresh device id and
// a fresh valid binding distinct from a prior identity.
func TestRekeyRotatesDeviceAndBinding(t *testing.T) {
	dir := t.TempDir()

	prior := &Identity{}
	priorSecret := make([]byte, 32)
	for i := range priorSecret {
		priorSecret[i] = byte(0xA0 + i)
	}
	if err := prior.SetFromSecret(priorSecret); err != nil {
		t.Fatal(err)
	}

	rekeyed, err := Rekey(dir)
	if err != nil {
		t.Fatal(err)
	}

	if rekeyed.DeviceID == "" || !deviceIDRe.MatchString(rekeyed.DeviceID) {
		t.Fatalf("rekeyed device id %q invalid", rekeyed.DeviceID)
	}
	if rekeyed.DeviceID == prior.DeviceID {
		t.Fatalf("rekey did not rotate device id: %q", rekeyed.DeviceID)
	}
	if rekeyed.BindingJSON == "" || rekeyed.BindingJSON == prior.BindingJSON {
		t.Fatal("rekey did not produce a fresh binding")
	}

	sb, err := identity.ParseSignedBinding([]byte(rekeyed.BindingJSON))
	if err != nil {
		t.Fatalf("parse rekeyed binding: %v", err)
	}
	if err := identity.VerifyBinding(sb); err != nil {
		t.Fatalf("verify rekeyed binding: %v", err)
	}
	if sb.Wallet != rekeyed.WalletAddress || sb.X25519 != rekeyed.OwnerPubHex || sb.Device != rekeyed.DeviceID {
		t.Fatalf("rekeyed binding fields mismatch: %+v", sb)
	}
}

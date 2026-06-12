package identity

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixed binding inputs for the vector: the B1.1 test wallet binds the X25519
// transport key derived from the same prf. ts is a fixed constant (no clock).
const (
	bindDevice = "a1b2c3d4e5f60718"
	bindX25519 = "269863f7f8d945c83cb429b6f16ab5655229a70b08272318267f41b1e8a28613" // owner X25519 pub for the same prf
	bindTs     = int64(1749600000)
)

func testWallet(t *testing.T) *Wallet {
	t.Helper()
	prf, _ := hex.DecodeString(walletPrfHex)
	w, err := DeriveWallet(prf)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSignVerifyRoundTrip(t *testing.T) {
	w := testWallet(t)
	sb, err := w.SignBinding(bindDevice, bindX25519, bindTs)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinding(sb); err != nil {
		t.Fatalf("verify own signature: %v", err)
	}
	// JSON round-trip then verify.
	wire, err := sb.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignedBinding([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinding(parsed); err != nil {
		t.Fatalf("verify parsed: %v", err)
	}
}

func TestTamperFailsVerify(t *testing.T) {
	w := testWallet(t)
	sb, _ := w.SignBinding(bindDevice, bindX25519, bindTs)
	bad := *sb
	bad.Device = "b1b2c3d4e5f60718" // flip one field; sig no longer matches
	if err := VerifyBinding(&bad); err == nil {
		t.Fatal("tampered binding verified")
	}
	bad2 := *sb
	bad2.Ts = sb.Ts + 1
	if err := VerifyBinding(&bad2); err == nil {
		t.Fatal("tampered ts verified")
	}
}

func TestRejectsUnsafeFields(t *testing.T) {
	w := testWallet(t)
	for _, dev := range []string{`a"b`, `a\b`, "a b", "a,b", ""} {
		if _, err := w.SignBinding(dev, bindX25519, bindTs); err == nil {
			t.Errorf("device %q should be rejected", dev)
		}
	}
	if _, err := w.SignBinding(bindDevice, "ZZZ", bindTs); err == nil {
		t.Error("non-hex x25519 should be rejected")
	}
}

func bindingVectorPath() string {
	return filepath.Join("..", "..", "..", "testdata", "wallet-binding.json")
}

type bindingVector struct {
	Wallet     string `json:"wallet"`
	WalletPriv string `json:"wallet_priv"`
	Device     string `json:"device"`
	X25519     string `json:"x25519"`
	Ts         int64  `json:"ts"`
	Canonical  string `json:"canonical"`
	Sig        string `json:"sig"`
	Record     string `json:"record"`
}

func TestBindingVector(t *testing.T) {
	w := testWallet(t)
	sb, err := w.SignBinding(bindDevice, bindX25519, bindTs)
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := sb.Binding.Canonical()
	record, _ := sb.JSON()
	got := bindingVector{
		Wallet:     w.Address,
		WalletPriv: hex.EncodeToString(w.Priv.Seed()),
		Device:     bindDevice,
		X25519:     bindX25519,
		Ts:         bindTs,
		Canonical:  canon,
		Sig:        sb.Sig,
		Record:     record,
	}

	path := bindingVectorPath()
	if os.Getenv("UPDATE_VECTORS") == "1" {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wallet-binding.json written")
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want bindingVector
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("binding drifted from committed vector\n got %+v\nwant %+v", got, want)
	}
}

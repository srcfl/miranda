// go/internal/identity/auth_test.go
package identity

import (
	"bytes"
	"testing"
)

func mustWallet(t *testing.T) *Wallet {
	t.Helper()
	prf := make([]byte, 32)
	for i := range prf {
		prf[i] = byte(i)
	}
	w, err := DeriveWallet(prf)
	if err != nil {
		t.Fatalf("DeriveWallet: %v", err)
	}
	return w
}

func TestSignAuthVerifies(t *testing.T) {
	w := mustWallet(t)
	challenge := []byte("a fresh channel binding")
	sig := w.SignAuth(challenge)
	if len(sig) != 64 {
		t.Fatalf("SignAuth len = %d, want 64", len(sig))
	}
	if err := VerifyAuth(w.Address, challenge, sig); err != nil {
		t.Fatalf("VerifyAuth(valid) = %v, want nil", err)
	}
}

func TestVerifyAuthRejectsTamperedChallenge(t *testing.T) {
	w := mustWallet(t)
	challenge := []byte("the real challenge")
	sig := w.SignAuth(challenge)
	tampered := bytes.Clone(challenge)
	tampered[0] ^= 0xff
	if err := VerifyAuth(w.Address, tampered, sig); err == nil {
		t.Fatal("VerifyAuth(tampered challenge) = nil, want error")
	}
}

func TestVerifyAuthRejectsTamperedSig(t *testing.T) {
	w := mustWallet(t)
	challenge := []byte("the real challenge")
	sig := w.SignAuth(challenge)
	sig[0] ^= 0xff
	if err := VerifyAuth(w.Address, challenge, sig); err == nil {
		t.Fatal("VerifyAuth(tampered sig) = nil, want error")
	}
}

func TestVerifyAuthRejectsWrongWallet(t *testing.T) {
	w := mustWallet(t)
	challenge := []byte("the real challenge")
	sig := w.SignAuth(challenge)

	prf2 := make([]byte, 32)
	for i := range prf2 {
		prf2[i] = byte(255 - i)
	}
	other, err := DeriveWallet(prf2)
	if err != nil {
		t.Fatalf("DeriveWallet: %v", err)
	}
	if err := VerifyAuth(other.Address, challenge, sig); err == nil {
		t.Fatal("VerifyAuth(wrong wallet) = nil, want error")
	}
}

func TestVerifyAuthRejectsBadWalletEncoding(t *testing.T) {
	w := mustWallet(t)
	sig := w.SignAuth([]byte("x"))
	if err := VerifyAuth("not-base58-0OIl", []byte("x"), sig); err == nil {
		t.Fatal("VerifyAuth(bad wallet encoding) = nil, want error")
	}
}

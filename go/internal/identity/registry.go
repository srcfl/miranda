// go/internal/identity/registry.go
package identity

import (
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// registrySalt domain-separates the registry AEAD key from any other use of the
// wallet secret. K_reg = HKDF-SHA256(wallet_secret, registrySalt, "aead-key").
const registrySalt = "miranda/registry/v1"

// RegistryKey derives the symmetric registry-encryption key from the wallet's
// 32-byte prf secret. Only wallet-holders can derive it; the relay never sees it.
// Mirrors web/src/identity/registry.js exactly.
func RegistryKey(secret []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, secret, []byte(registrySalt), []byte("aead-key"))
	k := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, k); err != nil {
		return nil, err
	}
	return k, nil
}

// SealRecord encrypts plaintext under key with machineID as AEAD associated data,
// returning nonce||ciphertext||tag. nonce must be 12 bytes (ChaCha20-Poly1305
// IETF). The machineID binds the blob to its registry slot.
func SealRecord(key, nonce, plaintext []byte, machineID string) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("registry: nonce must be %d bytes", aead.NonceSize())
	}
	ct := aead.Seal(nil, nonce, plaintext, []byte(machineID))
	return append(append([]byte{}, nonce...), ct...), nil
}

// OpenRecord reverses SealRecord. It returns an error (never partial plaintext)
// on any failure — a forged/garbage blob, or a wrong machineID (AAD), fails here.
func OpenRecord(key, blob []byte, machineID string) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(blob) < n {
		return nil, fmt.Errorf("registry: short blob")
	}
	return aead.Open(nil, blob[:n], blob[n:], []byte(machineID))
}

// go/internal/noise/keys.go
package noise

import (
	"crypto/rand"

	"golang.org/x/crypto/curve25519"
)

// GenerateStatic returns a fresh X25519 static keypair (32-byte priv and pub).
func GenerateStatic() (priv, pub []byte, err error) {
	priv = make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return nil, nil, err
	}
	pub, err = PublicFromPrivate(priv)
	return priv, pub, err
}

// PublicFromPrivate derives the X25519 public key for a 32-byte private key.
// X25519 clamping is applied by curve25519.X25519, matching RFC 7748 and the
// browser's @noble x25519.getPublicKey, so the same priv yields the same pub.
func PublicFromPrivate(priv []byte) ([]byte, error) {
	return curve25519.X25519(priv, curve25519.Basepoint)
}

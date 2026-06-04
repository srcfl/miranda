// go/internal/identity/owner.go
package identity

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

var (
	ownerSalt = []byte("terminal-relay/owner/v1")
	ownerInfo = []byte("x25519")
)

// DeriveOwnerKey turns a WebAuthn prf output into the stable owner X25519
// keypair. The same prf output (reproduced on every device by the synced
// passkey) yields the same owner_id. Mirrors web/src/identity/owner.js exactly.
func DeriveOwnerKey(prfOutput []byte) (priv, pub []byte, err error) {
	r := hkdf.New(sha256.New, prfOutput, ownerSalt, ownerInfo)
	priv = make([]byte, 32)
	if _, err = io.ReadFull(r, priv); err != nil {
		return nil, nil, err
	}
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	return priv, pub, err
}

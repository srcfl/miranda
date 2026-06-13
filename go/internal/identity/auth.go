// go/internal/identity/auth.go — wallet control proof over a fresh challenge
// (e.g. a pairing channel binding). Mirrors web/src/identity/auth.js.
package identity

import (
	"crypto/ed25519"
	"fmt"

	"github.com/srcful/terminal-relay/go/internal/base58"
)

// AuthDomain separates auth signatures from binding signatures and any other use
// of the wallet key. Signed bytes = AuthDomain || challenge.
const AuthDomain = "miranda/auth/v1"

func authMessage(challenge []byte) []byte { return append([]byte(AuthDomain), challenge...) }

// SignAuth proves control of the wallet over a fresh challenge. Returns the raw
// 64-byte Ed25519 signature.
func (w *Wallet) SignAuth(challenge []byte) []byte {
	return ed25519.Sign(w.Priv, authMessage(challenge))
}

// VerifyAuth checks a SignAuth signature against a base58 wallet address.
func VerifyAuth(walletBase58 string, challenge, sig []byte) error {
	pub, err := base58.Decode(walletBase58)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("auth: bad wallet key")
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pub), authMessage(challenge), sig) {
		return fmt.Errorf("auth: signature does not verify")
	}
	return nil
}

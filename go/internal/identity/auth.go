// go/internal/identity/auth.go — owner control proof over a fresh challenge
// (e.g. a pairing channel binding). Mirrors web/src/identity/auth.js.
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"

	"github.com/srcful/terminal-relay/go/internal/base58"
)

// AuthDomain separates auth signatures from binding signatures and any other use
// of the owner signing key. Signed bytes = AuthDomain || challenge.
const AuthDomain = "miranda/auth/v1"

const attachDomain = "miranda/attach/v1"

const registrationDomain = "miranda/agent-registration/v1"

func authMessage(challenge []byte) []byte { return append([]byte(AuthDomain), challenge...) }

// SignAuth proves control of the owner identity over a fresh challenge. Returns the raw
// 64-byte Ed25519 signature.
func (s *Signer) SignAuth(challenge []byte) []byte {
	return ed25519.Sign(s.Priv, authMessage(challenge))
}

// VerifyAuth checks a SignAuth signature against a base58 Miranda owner id.
func VerifyAuth(ownerID string, challenge, sig []byte) error {
	pub, err := base58.Decode(ownerID)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("auth: bad owner key")
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pub), authMessage(challenge), sig) {
		return fmt.Errorf("auth: signature does not verify")
	}
	return nil
}

// AttachChallenge binds an owner signature to one relay-issued session, one
// machine, and the exact SDP offer. An observed signature cannot authorize a
// fresh session or a modified offer. Mirrors web/src/identity/auth.js.
func AttachChallenge(session, machineID, sdp string) []byte {
	h := sha256.Sum256([]byte(sdp))
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%x", attachDomain, session, machineID, h))
}

// RegistrationChallenge lets an owner authorize one agent registration secret
// for one machine. The secret itself is never sent through pairing: only its
// SHA-256 commitment is signed. The relay later verifies the owner signature
// and checks that the presented secret opens the commitment.
func RegistrationChallenge(machineID, commitment string) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%s", registrationDomain, machineID, commitment))
}

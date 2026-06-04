// go/internal/noise/noise.go
package noise

import (
	"crypto/rand"
	"io"

	"github.com/flynn/noise"
)

var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

const prologue = "terminal-relay/v1"

// Handshake drives a Noise_KK_25519_ChaChaPoly_SHA256 handshake to completion.
type Handshake struct {
	hs        *noise.HandshakeState
	initiator bool
	done      bool
	send      *noise.CipherState
	recv      *noise.CipherState
}

// Session is the established encrypted channel after the handshake completes.
type Session struct {
	send *noise.CipherState
	recv *noise.CipherState
}

// NewInitiator/NewResponder build a KK handshake with production randomness.
func NewInitiator(staticPriv, peerStaticPub []byte) (*Handshake, error) {
	return newHandshake(true, staticPriv, peerStaticPub, rand.Reader)
}

func NewResponder(staticPriv, peerStaticPub []byte) (*Handshake, error) {
	return newHandshake(false, staticPriv, peerStaticPub, rand.Reader)
}

// newHandshake is the testable core; rng sources the ephemeral key (a fixed
// reader makes the handshake deterministic for interop vectors).
func newHandshake(initiator bool, staticPriv, peerStaticPub []byte, rng io.Reader) (*Handshake, error) {
	pub, err := PublicFromPrivate(staticPriv)
	if err != nil {
		return nil, err
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeKK,
		Initiator:     initiator,
		Prologue:      []byte(prologue),
		StaticKeypair: noise.DHKey{Private: staticPriv, Public: pub},
		PeerStatic:    peerStaticPub,
		Random:        rng,
	})
	if err != nil {
		return nil, err
	}
	return &Handshake{hs: hs, initiator: initiator}, nil
}

// WriteMessage emits the next handshake message carrying an optional payload.
func (h *Handshake) WriteMessage(payload []byte) ([]byte, error) {
	msg, cs0, cs1, err := h.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, err
	}
	if cs0 != nil {
		h.complete(cs0, cs1)
	}
	return msg, nil
}

// ReadMessage consumes the next handshake message, returning its payload.
func (h *Handshake) ReadMessage(message []byte) ([]byte, error) {
	payload, cs0, cs1, err := h.hs.ReadMessage(nil, message)
	if err != nil {
		return nil, err
	}
	if cs0 != nil {
		h.complete(cs0, cs1)
	}
	return payload, nil
}

func (h *Handshake) complete(cs0, cs1 *noise.CipherState) {
	// flynn/noise returns (cs_for_initiator_send, cs_for_responder_send).
	if h.initiator {
		h.send, h.recv = cs0, cs1
	} else {
		h.send, h.recv = cs1, cs0
	}
	h.done = true
}

func (h *Handshake) Done() bool        { return h.done }
func (h *Handshake) Session() *Session { return &Session{send: h.send, recv: h.recv} }

// Encrypt/Decrypt use empty AAD, matching the browser transport layer.
func (s *Session) Encrypt(plaintext []byte) ([]byte, error) {
	return s.send.Encrypt(nil, nil, plaintext)
}

func (s *Session) Decrypt(ciphertext []byte) ([]byte, error) {
	return s.recv.Decrypt(nil, nil, ciphertext)
}

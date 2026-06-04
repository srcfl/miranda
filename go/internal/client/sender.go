// go/internal/client/sender.go
package client

import (
	"sync"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// sender serializes encrypt+send for one Noise session. noise.Session.Encrypt
// mutates a nonce counter and is NOT safe for concurrent use, so every frame for
// a given session must go through one sender.
type sender struct {
	mc   peer.MsgConn
	sess *noise.Session
	mu   sync.Mutex
}

func newSender(mc peer.MsgConn, sess *noise.Session) *sender {
	return &sender{mc: mc, sess: sess}
}

// send encrypts an already-framed payload and writes it, holding the lock across
// both so the nonce order matches the ciphertext order on the wire.
func (s *sender) send(framed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ct, err := s.sess.Encrypt(framed)
	if err != nil {
		return err
	}
	return s.mc.Send(ct)
}

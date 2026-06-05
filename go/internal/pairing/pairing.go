// go/internal/pairing/pairing.go
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/flynn/noise"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

const prologue = "terminal-relay/pair/v1"

// AgentInfo is what the agent reveals to the client during pairing.
type AgentInfo struct {
	HostPubHex string `json:"host_pub"`
	MachineID  string `json:"machine_id"`
	Name       string `json:"name"`
}

func newHandshake(initiator bool, token []byte) (*noise.HandshakeState, error) {
	return noise.NewHandshakeState(noise.Config{
		CipherSuite:           cipherSuite,
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		Prologue:              []byte(prologue),
		PresharedKey:          pskFromToken(token),
		PresharedKeyPlacement: 0, // NNpsk0
		Random:                rand.Reader,
	})
}

// RunInitiator is the client side: it sends ownerPub and returns the agent's info.
func RunInitiator(ctx context.Context, mc peer.MsgConn, token, ownerPub []byte) (*AgentInfo, error) {
	hs, err := newHandshake(true, token)
	if err != nil {
		return nil, err
	}
	msg1, _, _, err := hs.WriteMessage(nil, ownerPub)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg1); err != nil {
		return nil, err
	}
	msg2, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	var info AgentInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RunResponder is the agent side: it returns the client's owner key and sends info.
func RunResponder(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) ([]byte, error) {
	hs, err := newHandshake(false, token)
	if err != nil {
		return nil, err
	}
	msg1, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	ownerPub, _, _, err := hs.ReadMessage(nil, msg1)
	if err != nil {
		return nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	infoJSON, _ := json.Marshal(info)
	msg2, _, _, err := hs.WriteMessage(nil, infoJSON)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg2); err != nil {
		return nil, err
	}
	return ownerPub, nil
}

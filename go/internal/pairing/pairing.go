// go/internal/pairing/pairing.go
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/flynn/noise"

	"github.com/srcful/terminal-relay/go/internal/base58"
	"github.com/srcful/terminal-relay/go/internal/identity"
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

// PairClaim is the initiator's msg1 payload: the base58 wallet it claims to own.
// The claim is later proven by a wallet auth signature over the channel binding
// (msg3), so the responder pins the wallet only after verifying control of it.
type PairClaim struct {
	Wallet string `json:"wallet"`
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

// RunInitiator is the client side: it sends a PairClaim for its wallet (msg1),
// reads the agent's info (msg2), then proves control of the wallet with an auth
// signature over the channel binding (msg3). It returns the agent's info plus the
// Noise channel binding (a transcript hash identical on both ends iff there was
// no MITM — use sas.FromBinding to show a safety number).
func RunInitiator(ctx context.Context, mc peer.MsgConn, token []byte, wallet *identity.Wallet) (*AgentInfo, []byte, error) {
	hs, err := newHandshake(true, token)
	if err != nil {
		return nil, nil, err
	}
	claim, _ := json.Marshal(PairClaim{Wallet: wallet.Address})
	msg1, _, _, err := hs.WriteMessage(nil, claim)
	if err != nil {
		return nil, nil, err
	}
	if err := mc.Send(msg1); err != nil {
		return nil, nil, err
	}
	msg2, err := mc.Recv(ctx)
	if err != nil {
		return nil, nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	var info AgentInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, nil, err
	}
	// msg3: prove control of the wallet by signing the channel binding. The
	// signature is public (it binds to the transcript hash), so it travels as a
	// plain frame outside the Noise payload.
	binding := hs.ChannelBinding()
	if err := mc.Send(wallet.SignAuth(binding)); err != nil {
		return nil, nil, err
	}
	return &info, binding, nil
}

// RunResponder is the agent side: it reads the client's PairClaim (msg1), sends
// its info (msg2), then verifies the client's wallet auth signature over the
// channel binding (msg3). It returns the proven base58 wallet plus the Noise
// channel binding (see RunInitiator). The wallet is returned only if auth
// verifies — callers pin it directly.
func RunResponder(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) (string, []byte, error) {
	hs, err := newHandshake(false, token)
	if err != nil {
		return "", nil, err
	}
	msg1, err := mc.Recv(ctx)
	if err != nil {
		return "", nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg1)
	if err != nil {
		return "", nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	var claim PairClaim
	if err := json.Unmarshal(payload, &claim); err != nil {
		return "", nil, fmt.Errorf("pairing: bad claim: %w", err)
	}
	if pk, derr := base58.Decode(claim.Wallet); derr != nil || len(pk) != 32 {
		return "", nil, fmt.Errorf("pairing: bad wallet")
	}
	infoJSON, _ := json.Marshal(info)
	msg2, _, _, err := hs.WriteMessage(nil, infoJSON)
	if err != nil {
		return "", nil, err
	}
	if err := mc.Send(msg2); err != nil {
		return "", nil, err
	}
	binding := hs.ChannelBinding()
	sig, err := mc.Recv(ctx) // msg3: the wallet auth signature over the binding
	if err != nil {
		return "", nil, err
	}
	if err := identity.VerifyAuth(claim.Wallet, binding, sig); err != nil {
		return "", nil, fmt.Errorf("pairing: wallet auth failed: %w", err)
	}
	return claim.Wallet, binding, nil
}

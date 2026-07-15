// go/internal/pairing/pairing.go
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	HostPubHex             string `json:"host_pub"`
	MachineID              string `json:"machine_id"`
	Name                   string `json:"name"`
	RegistrationCommitment string `json:"registration_commitment,omitempty"`
}

// PairClaim is the initiator's msg1 payload. Wallet is the historical wire field
// name; its value is the base58 Miranda owner id. The responder pins it only
// after verifying an owner signature over the channel binding in msg3.
type PairClaim struct {
	Wallet string `json:"wallet"`
}

// PairProof is msg3 in the provisioned flow. Signature proves owner control
// over the Noise transcript; Registry is an optional owner-encrypted discovery
// record for this machine. The agent can publish it but cannot decrypt it.
type PairProof struct {
	Signature        string `json:"signature"`
	Registry         string `json:"registry,omitempty"`
	RegistrationAuth string `json:"registration_auth,omitempty"`
}

// Provision is owner-created state delivered to the agent only after the
// pairing transcript signature verifies. Neither field is an owner secret.
type Provision struct {
	Registry         string
	RegistrationAuth string
}

type Provisioner func(*AgentInfo) (string, error)

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

// RunInitiator is the client side: it sends its owner claim (msg1), reads the
// agent's info (msg2), then proves control of the owner identity with an auth
// signature over the channel binding (msg3). It returns the agent's info plus the
// Noise channel binding (a transcript hash identical on both ends iff there was
// no MITM — use sas.FromBinding to show a safety number).
func RunInitiator(ctx context.Context, mc peer.MsgConn, token []byte, signer *identity.Signer) (*AgentInfo, []byte, error) {
	return runInitiator(ctx, mc, token, signer, nil)
}

// RunInitiatorProvisioned performs owner-side discovery provisioning without
// sending the owner secret or registry key to the agent.
func RunInitiatorProvisioned(ctx context.Context, mc peer.MsgConn, token []byte, signer *identity.Signer, provision Provisioner) (*AgentInfo, []byte, error) {
	return runInitiator(ctx, mc, token, signer, provision)
}

func runInitiator(ctx context.Context, mc peer.MsgConn, token []byte, signer *identity.Signer, provision Provisioner) (*AgentInfo, []byte, error) {
	hs, err := newHandshake(true, token)
	if err != nil {
		return nil, nil, err
	}
	claim, _ := json.Marshal(PairClaim{Wallet: signer.Address})
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
	// msg3: prove owner control and optionally provision an opaque record.
	binding := hs.ChannelBinding()
	sig := signer.SignAuth(binding)
	if provision == nil {
		// Compatibility with v0.6 responders.
		if err := mc.Send(sig); err != nil {
			return nil, nil, err
		}
		return &info, binding, nil
	}
	registry, err := provision(&info)
	if err != nil {
		return nil, nil, err
	}
	registrationAuth := ""
	if info.RegistrationCommitment != "" {
		registrationAuth = base64.StdEncoding.EncodeToString(signer.SignAuth(identity.RegistrationChallenge(info.MachineID, info.RegistrationCommitment)))
	}
	proof, err := json.Marshal(PairProof{
		Signature:        base64.StdEncoding.EncodeToString(sig),
		Registry:         registry,
		RegistrationAuth: registrationAuth,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := mc.Send(proof); err != nil {
		return nil, nil, err
	}
	return &info, binding, nil
}

// RunResponder is the agent side: it reads the client's PairClaim (msg1), sends
// its info (msg2), then verifies the client's owner signature over the channel
// binding (msg3). It returns the proven base58 owner id plus the Noise channel
// binding. The owner id is returned only if auth verifies.
func RunResponder(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) (string, []byte, error) {
	ownerID, _, binding, err := runResponder(ctx, mc, token, info)
	return ownerID, binding, err
}

// RunResponderProvisioned returns the opaque record only after owner auth has
// verified. Callers may persist it next to the owner pin.
func RunResponderProvisioned(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) (string, Provision, []byte, error) {
	return runResponder(ctx, mc, token, info)
}

func runResponder(ctx context.Context, mc peer.MsgConn, token []byte, info AgentInfo) (string, Provision, []byte, error) {
	hs, err := newHandshake(false, token)
	if err != nil {
		return "", Provision{}, nil, err
	}
	msg1, err := mc.Recv(ctx)
	if err != nil {
		return "", Provision{}, nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg1)
	if err != nil {
		return "", Provision{}, nil, fmt.Errorf("pairing handshake failed (wrong code?): %w", err)
	}
	var claim PairClaim
	if err := json.Unmarshal(payload, &claim); err != nil {
		return "", Provision{}, nil, fmt.Errorf("pairing: bad claim: %w", err)
	}
	if pk, derr := base58.Decode(claim.Wallet); derr != nil || len(pk) != 32 {
		return "", Provision{}, nil, fmt.Errorf("pairing: bad owner id")
	}
	infoJSON, _ := json.Marshal(info)
	msg2, _, _, err := hs.WriteMessage(nil, infoJSON)
	if err != nil {
		return "", Provision{}, nil, err
	}
	if err := mc.Send(msg2); err != nil {
		return "", Provision{}, nil, err
	}
	binding := hs.ChannelBinding()
	proofBytes, err := mc.Recv(ctx)
	if err != nil {
		return "", Provision{}, nil, err
	}
	sig := proofBytes
	provision := Provision{}
	if len(proofBytes) != 64 {
		var proof PairProof
		if err := json.Unmarshal(proofBytes, &proof); err != nil {
			return "", Provision{}, nil, fmt.Errorf("pairing: bad owner proof")
		}
		sig, err = base64.StdEncoding.DecodeString(proof.Signature)
		if err != nil {
			return "", Provision{}, nil, fmt.Errorf("pairing: bad owner signature encoding")
		}
		provision.Registry = proof.Registry
		provision.RegistrationAuth = proof.RegistrationAuth
	}
	if err := identity.VerifyAuth(claim.Wallet, binding, sig); err != nil {
		return "", Provision{}, nil, fmt.Errorf("pairing: owner auth failed: %w", err)
	}
	if info.RegistrationCommitment != "" {
		registrationSig, err := base64.StdEncoding.DecodeString(provision.RegistrationAuth)
		if err != nil || identity.VerifyAuth(claim.Wallet, identity.RegistrationChallenge(info.MachineID, info.RegistrationCommitment), registrationSig) != nil {
			return "", Provision{}, nil, fmt.Errorf("pairing: invalid agent registration authorization")
		}
	}
	return claim.Wallet, provision, binding, nil
}

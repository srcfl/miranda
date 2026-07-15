package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const revocationDomain = "miranda/revocation/v1"

var machineIDRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,128}$`)

// Revocation is a permanent owner-signed tombstone for one machine id. A relay
// can suppress it (availability is never trusted), but cannot forge one.
type Revocation struct {
	V         int    `json:"v"`
	OwnerID   string `json:"owner_id"`
	MachineID string `json:"machine_id"`
	TS        int64  `json:"ts"`
	Signature string `json:"signature"`
}

func RevocationChallenge(machineID string, ts int64) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%d", revocationDomain, machineID, ts))
}

func (s *Signer) SignRevocation(machineID string, ts time.Time) (*Revocation, error) {
	if !machineIDRe.MatchString(machineID) {
		return nil, fmt.Errorf("revocation: invalid machine id")
	}
	unix := ts.Unix()
	if unix <= 0 {
		return nil, fmt.Errorf("revocation: invalid timestamp")
	}
	sig := s.SignAuth(RevocationChallenge(machineID, unix))
	return &Revocation{V: 1, OwnerID: s.Address, MachineID: machineID, TS: unix, Signature: base64.StdEncoding.EncodeToString(sig)}, nil
}

func VerifyRevocation(r *Revocation) error {
	if r == nil || r.V != 1 || !machineIDRe.MatchString(r.MachineID) || r.TS <= 0 {
		return fmt.Errorf("revocation: invalid record")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return fmt.Errorf("revocation: invalid signature encoding")
	}
	if err := VerifyAuth(r.OwnerID, RevocationChallenge(r.MachineID, r.TS), sig); err != nil {
		return fmt.Errorf("revocation: owner signature failed: %w", err)
	}
	return nil
}

func (r Revocation) JSON() ([]byte, error) { return json.Marshal(r) }

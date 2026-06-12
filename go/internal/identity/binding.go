// go/internal/identity/binding.go
package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/base58"
)

// bindingDomain separates binding signatures from any other use of the wallet
// key (auth, future schemes). Signed bytes = bindingDomain || canonical(binding).
const bindingDomain = "miranda/binding/v1"

var (
	deviceRe = regexp.MustCompile(`^[0-9A-Za-z._-]+$`)
	hex64Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Binding authorizes a device's X25519 transport key under the wallet identity.
// A wallet-addressed peer presents this so others can accept its Noise-KK static
// key. Mirrors web/src/identity/binding.js exactly.
type Binding struct {
	V      int    // version, always 1
	Wallet string // base58 Ed25519 wallet address (the signer)
	Device string // machine_id
	X25519 string // hex of the 32-byte transport public key it authorizes
	Ts     int64  // unix seconds
}

// SignedBinding is a Binding plus the wallet's base58 signature.
type SignedBinding struct {
	Binding
	Sig string
}

func (b Binding) validate() error {
	if b.V != 1 {
		return fmt.Errorf("binding: unsupported version %d", b.V)
	}
	if pk, err := base58.Decode(b.Wallet); err != nil || len(pk) != ed25519.PublicKeySize {
		return fmt.Errorf("binding: wallet is not a 32-byte base58 key")
	}
	if !deviceRe.MatchString(b.Device) {
		return fmt.Errorf("binding: device has unsafe characters")
	}
	if !hex64Re.MatchString(b.X25519) {
		return fmt.Errorf("binding: x25519 must be 64 lowercase hex chars")
	}
	if b.Ts <= 0 {
		return fmt.Errorf("binding: ts must be positive")
	}
	return nil
}

// Canonical returns the byte-identical signing message: fixed field order, no
// whitespace. Fields are validated to need no JSON escaping, so this is built by
// concatenation (not encoding/json, which HTML-escapes <>&) and matches JS
// JSON.stringify of the same object byte-for-byte.
func (b Binding) Canonical() (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(`{"v":`)
	sb.WriteString(strconv.Itoa(b.V))
	sb.WriteString(`,"wallet":"`)
	sb.WriteString(b.Wallet)
	sb.WriteString(`","device":"`)
	sb.WriteString(b.Device)
	sb.WriteString(`","x25519":"`)
	sb.WriteString(b.X25519)
	sb.WriteString(`","ts":`)
	sb.WriteString(strconv.FormatInt(b.Ts, 10))
	sb.WriteString(`}`)
	return sb.String(), nil
}

func bindingMessage(canonical string) []byte {
	return append([]byte(bindingDomain), canonical...)
}

// SignBinding builds and signs a binding authorizing device + x25519 (the 32-byte
// transport pub, hex) under this wallet.
func (w *Wallet) SignBinding(device, x25519 string, ts int64) (*SignedBinding, error) {
	b := Binding{V: 1, Wallet: w.Address, Device: device, X25519: x25519, Ts: ts}
	canon, err := b.Canonical()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(w.Priv, bindingMessage(canon))
	return &SignedBinding{Binding: b, Sig: base58.Encode(sig)}, nil
}

// VerifyBinding checks the signature against the wallet public key embedded in
// the binding. Returns nil iff valid.
func VerifyBinding(sb *SignedBinding) error {
	canon, err := sb.Binding.Canonical()
	if err != nil {
		return err
	}
	pub, err := base58.Decode(sb.Wallet)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("binding: bad wallet key")
	}
	sig, err := base58.Decode(sb.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("binding: bad signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), bindingMessage(canon), sig) {
		return fmt.Errorf("binding: signature does not verify")
	}
	return nil
}

// JSON renders the wire record: the canonical binding with ,"sig":"…" appended
// before the closing brace. Sig is base58, so no escaping is needed.
func (sb *SignedBinding) JSON() (string, error) {
	canon, err := sb.Binding.Canonical()
	if err != nil {
		return "", err
	}
	return canon[:len(canon)-1] + `,"sig":"` + sb.Sig + `"}`, nil
}

type wireBinding struct {
	V      int    `json:"v"`
	Wallet string `json:"wallet"`
	Device string `json:"device"`
	X25519 string `json:"x25519"`
	Ts     int64  `json:"ts"`
	Sig    string `json:"sig"`
}

// ParseSignedBinding parses a wire record (the JSON above). It does not verify
// the signature; call VerifyBinding on the result.
func ParseSignedBinding(data []byte) (*SignedBinding, error) {
	var w wireBinding
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("binding: bad JSON: %w", err)
	}
	return &SignedBinding{
		Binding: Binding{V: w.V, Wallet: w.Wallet, Device: w.Device, X25519: w.X25519, Ts: w.Ts},
		Sig:     w.Sig,
	}, nil
}

// go/internal/identity/grant.go
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/base58"
)

// grantDomain separates grant signatures from bindings, auth, and revocations
// under the same owner key. Signed bytes = grantDomain || canonical(grant).
const grantDomain = "miranda/grant/v1"

// Grant TTL bounds (G1 spec §1): default one hour, hard cap 24 h so the
// revocation backstop is real; nb carries five minutes of clock-skew
// tolerance, matching mir doctor's threshold.
const (
	GrantDefaultTTL = time.Hour
	GrantMaxTTL     = 24 * time.Hour
	GrantSkew       = 5 * time.Minute
)

var (
	scopeRe = regexp.MustCompile(`^[0-9A-Za-z._-]{1,64}$`)
	gidRe   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Grant authorizes one guest key to reach one machine's terminal, mode- and
// time-bounded. It is signed by the minting owner and verified by the agent
// against the owner keys it already pins — a grant from an unpinned owner is
// dead, which also invalidates every old grant on identity rotation.
// Mirrors web/src/identity/grant.js exactly.
type Grant struct {
	V       int    // version, always 1
	Owner   string // base58 Ed25519 owner id (must be pinned on the agent)
	Machine string // machine_id
	Guest   string // base58 Ed25519 guest id, bound at claim time — never a bearer token
	Scope   string // tmux session name the share covers
	Mode    string // "ro" | "rw"
	NB      int64  // not before, unix seconds (mint time − GrantSkew)
	NA      int64  // not after, unix seconds (mint time + TTL)
	GID     string // 16 lowercase hex, names the grant for revocation
}

// SignedGrant is a Grant plus the owner's base58 signature.
type SignedGrant struct {
	Grant
	Sig string
}

func (g Grant) validate() error {
	if g.V != 1 {
		return fmt.Errorf("grant: unsupported version %d", g.V)
	}
	if pk, err := base58.Decode(g.Owner); err != nil || len(pk) != ed25519.PublicKeySize {
		return fmt.Errorf("grant: owner is not a 32-byte base58 key")
	}
	if !machineIDRe.MatchString(g.Machine) {
		return fmt.Errorf("grant: invalid machine id")
	}
	if pk, err := base58.Decode(g.Guest); err != nil || len(pk) != ed25519.PublicKeySize {
		return fmt.Errorf("grant: guest is not a 32-byte base58 key")
	}
	if !scopeRe.MatchString(g.Scope) {
		return fmt.Errorf("grant: invalid scope")
	}
	if g.Mode != "ro" && g.Mode != "rw" {
		return fmt.Errorf("grant: mode must be ro or rw")
	}
	if g.NB <= 0 {
		return fmt.Errorf("grant: nb must be positive")
	}
	if g.NA <= g.NB {
		return fmt.Errorf("grant: na must be after nb")
	}
	if g.NA-g.NB > int64((GrantMaxTTL + GrantSkew).Seconds()) {
		return fmt.Errorf("grant: window exceeds the %v cap", GrantMaxTTL)
	}
	if !gidRe.MatchString(g.GID) {
		return fmt.Errorf("grant: gid must be 16 lowercase hex chars")
	}
	return nil
}

// Canonical returns the byte-identical signing message: fixed field order, no
// whitespace. Fields are validated to need no JSON escaping, so this is built
// by concatenation (not encoding/json, which HTML-escapes <>&) and matches JS
// byte-for-byte.
func (g Grant) Canonical() (string, error) {
	if err := g.validate(); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(`{"v":`)
	sb.WriteString(strconv.Itoa(g.V))
	sb.WriteString(`,"owner":"`)
	sb.WriteString(g.Owner)
	sb.WriteString(`","machine":"`)
	sb.WriteString(g.Machine)
	sb.WriteString(`","guest":"`)
	sb.WriteString(g.Guest)
	sb.WriteString(`","scope":"`)
	sb.WriteString(g.Scope)
	sb.WriteString(`","mode":"`)
	sb.WriteString(g.Mode)
	sb.WriteString(`","nb":`)
	sb.WriteString(strconv.FormatInt(g.NB, 10))
	sb.WriteString(`,"na":`)
	sb.WriteString(strconv.FormatInt(g.NA, 10))
	sb.WriteString(`,"gid":"`)
	sb.WriteString(g.GID)
	sb.WriteString(`"}`)
	return sb.String(), nil
}

func grantMessage(canonical string) []byte {
	return append([]byte(grantDomain), canonical...)
}

// SignGrant signs a fully specified grant. MintGrant is the production entry;
// this level exists so the interop vector can pin fixed fields.
func (s *Signer) SignGrant(g Grant) (*SignedGrant, error) {
	if g.Owner != s.Address {
		return nil, fmt.Errorf("grant: owner field must be the signing identity")
	}
	canon, err := g.Canonical()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(s.Priv, grantMessage(canon))
	return &SignedGrant{Grant: g, Sig: base58.Encode(sig)}, nil
}

// MintGrant builds and signs a grant for guest on machine. Zero scope means
// "main", zero mode means "ro". TTL is bounded (0, GrantMaxTTL]; nb carries
// GrantSkew of tolerance so a slightly-behind agent clock accepts a fresh
// grant.
func MintGrant(s *Signer, machine, guest, scope, mode string, ttl time.Duration, now time.Time) (*SignedGrant, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("grant: ttl must be positive")
	}
	if ttl > GrantMaxTTL {
		return nil, fmt.Errorf("grant: ttl exceeds the %v cap", GrantMaxTTL)
	}
	if scope == "" {
		scope = "main"
	}
	if mode == "" {
		mode = "ro"
	}
	gid := make([]byte, 8)
	if _, err := rand.Read(gid); err != nil {
		return nil, err
	}
	g := Grant{
		V:       1,
		Owner:   s.Address,
		Machine: machine,
		Guest:   guest,
		Scope:   scope,
		Mode:    mode,
		NB:      now.Add(-GrantSkew).Unix(),
		NA:      now.Add(ttl).Unix(),
		GID:     hex.EncodeToString(gid),
	}
	return s.SignGrant(g)
}

// VerifyGrant checks the signature against the owner key embedded in the
// grant. Record-level only: the agent must additionally require that the
// embedded owner is currently pinned, and check ValidAt on every attach.
func VerifyGrant(sg *SignedGrant) error {
	canon, err := sg.Grant.Canonical()
	if err != nil {
		return err
	}
	pub, err := base58.Decode(sg.Owner)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("grant: bad owner key")
	}
	sig, err := base58.Decode(sg.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("grant: bad signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), grantMessage(canon), sig) {
		return fmt.Errorf("grant: signature does not verify")
	}
	return nil
}

// ValidAt reports whether the grant's time window covers now. Signature and
// pin checks are separate; the agent runs all three on every attach.
func (g Grant) ValidAt(now time.Time) error {
	t := now.Unix()
	if t < g.NB {
		return fmt.Errorf("grant: not valid yet")
	}
	if t > g.NA {
		return fmt.Errorf("grant: expired")
	}
	return nil
}

// JSON renders the wire record: the canonical grant with ,"sig":"…" appended
// before the closing brace. Sig is base58, so no escaping is needed.
func (sg *SignedGrant) JSON() (string, error) {
	canon, err := sg.Grant.Canonical()
	if err != nil {
		return "", err
	}
	return canon[:len(canon)-1] + `,"sig":"` + sg.Sig + `"}`, nil
}

type wireGrant struct {
	V       int    `json:"v"`
	Owner   string `json:"owner"`
	Machine string `json:"machine"`
	Guest   string `json:"guest"`
	Scope   string `json:"scope"`
	Mode    string `json:"mode"`
	NB      int64  `json:"nb"`
	NA      int64  `json:"na"`
	GID     string `json:"gid"`
	Sig     string `json:"sig"`
}

// ParseSignedGrant parses a wire record. It does not verify the signature;
// call VerifyGrant on the result.
func ParseSignedGrant(data []byte) (*SignedGrant, error) {
	var w wireGrant
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("grant: bad JSON: %w", err)
	}
	return &SignedGrant{
		Grant: Grant{V: w.V, Owner: w.Owner, Machine: w.Machine, Guest: w.Guest,
			Scope: w.Scope, Mode: w.Mode, NB: w.NB, NA: w.NA, GID: w.GID},
		Sig: w.Sig,
	}, nil
}

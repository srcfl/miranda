package identity

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/base58"
)

// Fixed grant inputs for the cross-language vector: the standard test signer
// mints for a guest derived from a fixed prf; all times are constants.
const (
	grantMachine = "a1b2c3d4e5f60718"
	grantScope   = "main"
	grantMode    = "ro"
	grantMint    = int64(1756512000) // 2026-08-30 00:00:00 UTC
	grantGID     = "00112233aabbccdd"
)

func testGuest(t *testing.T) *Signer {
	t.Helper()
	g, err := DeriveSigner(bytes.Repeat([]byte{0x44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func fixedGrant(t *testing.T) Grant {
	t.Helper()
	return Grant{
		V: 1, Owner: testSigner(t).Address, Machine: grantMachine,
		Guest: testGuest(t).Address, Scope: grantScope, Mode: grantMode,
		NB: grantMint - int64(GrantSkew.Seconds()), NA: grantMint + 3600, GID: grantGID,
	}
}

func TestGrantSignVerifyRoundTrip(t *testing.T) {
	w := testSigner(t)
	sg, err := w.SignGrant(fixedGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyGrant(sg); err != nil {
		t.Fatalf("verify own signature: %v", err)
	}
	wire, err := sg.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSignedGrant([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyGrant(parsed); err != nil {
		t.Fatalf("verify parsed: %v", err)
	}
}

func TestGrantTamperFailsVerify(t *testing.T) {
	w := testSigner(t)
	sg, _ := w.SignGrant(fixedGrant(t))
	mutations := []func(*SignedGrant){
		func(s *SignedGrant) { s.Machine = "b1b2c3d4e5f60718" },
		func(s *SignedGrant) { s.Guest = s.Owner },
		func(s *SignedGrant) { s.Scope = "other" },
		func(s *SignedGrant) { s.Mode = "rw" },
		func(s *SignedGrant) { s.NB++ },
		func(s *SignedGrant) { s.NA-- },
		func(s *SignedGrant) { s.GID = "ffffffffffffffff" },
		func(s *SignedGrant) { s.Sig = "1111111111" },
	}
	for i, mutate := range mutations {
		bad := *sg
		mutate(&bad)
		if err := VerifyGrant(&bad); err == nil {
			t.Errorf("mutation %d verified", i)
		}
	}
}

// A signature minted under a different domain tag must not verify as a grant,
// even over the identical canonical bytes.
func TestGrantRejectsForeignDomainSignature(t *testing.T) {
	w := testSigner(t)
	g := fixedGrant(t)
	canon, err := g.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig := w.SignAuth([]byte("miranda/binding/v1" + canon))
	sg := &SignedGrant{Grant: g, Sig: base58.Encode(sig)}
	if err := VerifyGrant(sg); err == nil {
		t.Fatal("foreign-domain signature verified")
	}
}

func TestGrantValidationRejects(t *testing.T) {
	base := fixedGrant(t)
	cases := []struct {
		name   string
		mutate func(*Grant)
	}{
		{"bad version", func(g *Grant) { g.V = 2 }},
		{"owner not base58", func(g *Grant) { g.Owner = `a"b` }},
		{"owner wrong length", func(g *Grant) { g.Owner = "abc" }},
		{"machine unsafe", func(g *Grant) { g.Machine = `m"1` }},
		{"machine empty", func(g *Grant) { g.Machine = "" }},
		{"guest not a key", func(g *Grant) { g.Guest = "zzz" }},
		{"scope unsafe", func(g *Grant) { g.Scope = `ma"in` }},
		{"scope too long", func(g *Grant) { g.Scope = strings.Repeat("a", 65) }},
		{"mode unknown", func(g *Grant) { g.Mode = "admin" }},
		{"nb zero", func(g *Grant) { g.NB = 0 }},
		{"na before nb", func(g *Grant) { g.NA = g.NB }},
		{"window over cap", func(g *Grant) { g.NA = g.NB + int64((GrantMaxTTL + GrantSkew).Seconds()) + 1 }},
		{"gid short", func(g *Grant) { g.GID = "0011" }},
		{"gid uppercase", func(g *Grant) { g.GID = "00112233AABBCCDD" }},
	}
	for _, tc := range cases {
		g := base
		tc.mutate(&g)
		if _, err := g.Canonical(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestMintGrantDefaultsAndBounds(t *testing.T) {
	w := testSigner(t)
	guest := testGuest(t).Address
	now := time.Unix(grantMint, 0)

	sg, err := MintGrant(w, grantMachine, guest, "", "", GrantDefaultTTL, now)
	if err != nil {
		t.Fatal(err)
	}
	if sg.Scope != "main" || sg.Mode != "ro" {
		t.Fatalf("defaults: got scope %q mode %q", sg.Scope, sg.Mode)
	}
	if sg.NB != grantMint-int64(GrantSkew.Seconds()) || sg.NA != grantMint+3600 {
		t.Fatalf("window: nb %d na %d", sg.NB, sg.NA)
	}
	if !gidRe.MatchString(sg.GID) {
		t.Fatalf("gid shape: %q", sg.GID)
	}
	sg2, _ := MintGrant(w, grantMachine, guest, "", "", GrantDefaultTTL, now)
	if sg.GID == sg2.GID {
		t.Fatal("two mints produced the same gid")
	}
	if err := VerifyGrant(sg); err != nil {
		t.Fatal(err)
	}

	if _, err := MintGrant(w, grantMachine, guest, "", "", 0, now); err == nil {
		t.Fatal("zero ttl accepted")
	}
	if _, err := MintGrant(w, grantMachine, guest, "", "", GrantMaxTTL+time.Second, now); err == nil {
		t.Fatal("over-cap ttl accepted")
	}
	if _, err := MintGrant(w, grantMachine, guest, "", "rwx", GrantDefaultTTL, now); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestGrantValidAt(t *testing.T) {
	g := fixedGrant(t)
	if err := g.ValidAt(time.Unix(g.NB-1, 0)); err == nil {
		t.Fatal("accepted before nb")
	}
	for _, ts := range []int64{g.NB, grantMint, g.NA} {
		if err := g.ValidAt(time.Unix(ts, 0)); err != nil {
			t.Fatalf("rejected inside window at %d: %v", ts, err)
		}
	}
	if err := g.ValidAt(time.Unix(g.NA+1, 0)); err == nil {
		t.Fatal("accepted after na")
	}
}

func TestSignGrantRefusesForeignOwnerField(t *testing.T) {
	g := fixedGrant(t)
	g.Owner = testGuest(t).Address // not the signing identity
	if _, err := testSigner(t).SignGrant(g); err == nil {
		t.Fatal("signed a grant naming someone else as owner")
	}
}

type grantVector struct {
	Owner     string `json:"owner"`
	OwnerPriv string `json:"owner_priv"`
	Guest     string `json:"guest"`
	GuestPriv string `json:"guest_priv"`
	Machine   string `json:"machine"`
	Scope     string `json:"scope"`
	Mode      string `json:"mode"`
	NB        int64  `json:"nb"`
	NA        int64  `json:"na"`
	GID       string `json:"gid"`
	Canonical string `json:"canonical"`
	Sig       string `json:"sig"`
	Record    string `json:"record"`
}

func TestGrantVector(t *testing.T) {
	w := testSigner(t)
	guest := testGuest(t)
	sg, err := w.SignGrant(fixedGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := sg.Grant.Canonical()
	record, _ := sg.JSON()
	got := grantVector{
		Owner: w.Address, OwnerPriv: hex.EncodeToString(w.Priv.Seed()),
		Guest: guest.Address, GuestPriv: hex.EncodeToString(guest.Priv.Seed()),
		Machine: grantMachine, Scope: grantScope, Mode: grantMode,
		NB: sg.NB, NA: sg.NA, GID: grantGID,
		Canonical: canon, Sig: sg.Sig, Record: record,
	}

	path := filepath.Join("..", "..", "..", "testdata", "grant.json")
	if os.Getenv("UPDATE_VECTORS") == "1" {
		data, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("grant.json written")
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vector (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want grantVector
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("grant drifted from committed vector\n got %+v\nwant %+v", got, want)
	}
}

// go/internal/client/shares_test.go — local share state: owner mint records,
// gid prefix resolution, guest grant lookup, and the guest-state sweep.
package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

func shareSigner(t *testing.T, fill byte) *identity.Signer {
	t.Helper()
	s, err := identity.DeriveSigner(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mintRecord(t *testing.T, owner, guest *identity.Signer, machine string, ttl time.Duration) (string, *identity.SignedGrant) {
	t.Helper()
	sg, err := identity.MintGrant(owner, machine, guest.Address, "", "", ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := sg.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return rec, sg
}

func TestOwnerShareRoundTripAndRevokedFlag(t *testing.T) {
	dir := t.TempDir()
	owner, guest := shareSigner(t, 0x11), shareSigner(t, 0x22)
	rec, sg := mintRecord(t, owner, guest, "machine-1", time.Hour)
	if err := SaveOwnerShare(dir, rec, "sharebox"); err != nil {
		t.Fatal(err)
	}
	shares, err := ListOwnerShares(dir)
	if err != nil || len(shares) != 1 {
		t.Fatalf("shares=%v err=%v", shares, err)
	}
	s := shares[0]
	if s.MachineName != "sharebox" || s.Revoked || s.Grant.GID != sg.GID {
		t.Fatalf("share = %+v", s)
	}
	if err := MarkShareRevoked(dir, sg.GID); err != nil {
		t.Fatal(err)
	}
	shares, _ = ListOwnerShares(dir)
	if !shares[0].Revoked {
		t.Fatal("revoked flag did not persist")
	}
}

func TestResolveShareGIDPrefix(t *testing.T) {
	dir := t.TempDir()
	owner, guest := shareSigner(t, 0x11), shareSigner(t, 0x22)
	recA, sgA := mintRecord(t, owner, guest, "machine-1", time.Hour)
	recB, sgB := mintRecord(t, owner, guest, "machine-2", 2*time.Hour)
	_ = SaveOwnerShare(dir, recA, "boxA")
	_ = SaveOwnerShare(dir, recB, "boxB")

	got, err := ResolveShareGID(dir, sgA.GID[:8])
	if err != nil || got.Grant.GID != sgA.GID {
		t.Fatalf("prefix resolve: %+v err=%v", got, err)
	}
	if _, err := ResolveShareGID(dir, "zzzz"); err == nil {
		t.Fatal("unknown prefix resolved")
	}
	// The empty prefix matches both → ambiguous.
	if _, err := ResolveShareGID(dir, ""); err == nil {
		t.Fatal("ambiguous prefix resolved")
	}
	_ = sgB
}

func TestGuestGrantForPicksLatest(t *testing.T) {
	dir := t.TempDir()
	owner, guest := shareSigner(t, 0x11), shareSigner(t, 0x22)
	recOld, _ := mintRecord(t, owner, guest, "m1", time.Hour)
	recNew, sgNew := mintRecord(t, owner, guest, "m1", 2*time.Hour)
	for _, rec := range []string{recOld, recNew} {
		sg, _ := identity.ParseSignedGrant([]byte(rec))
		if err := SaveGuestGrant(dir, sg.GID, rec); err != nil {
			t.Fatal(err)
		}
	}
	g := GuestGrantFor(dir, "m1")
	if g == nil || g.GID != sgNew.GID {
		t.Fatalf("want the later grant, got %+v", g)
	}
	if GuestGrantFor(dir, "other") != nil {
		t.Fatal("grant for a machine we never joined")
	}
}

func TestSweepGuestStateRemovesClosedShares(t *testing.T) {
	dir := t.TempDir()
	owner, guest := shareSigner(t, 0x11), shareSigner(t, 0x22)

	// One live share, one whose window has fully closed.
	liveRec, liveSG := mintRecord(t, owner, guest, "m-live", time.Hour)
	_ = SaveGuestGrant(dir, liveSG.GID, liveRec)
	now := time.Now()
	dead, err := owner.SignGrant(identity.Grant{
		V: 1, Owner: owner.Address, Machine: "m-dead", Guest: guest.Address,
		Scope: "main", Mode: "ro", NB: now.Add(-3 * time.Hour).Unix(), NA: now.Add(-2 * time.Hour).Unix(),
		GID: "deaddeaddeaddead",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadRec, _ := dead.JSON()
	_ = SaveGuestGrant(dir, dead.GID, deadRec)

	for _, m := range []Machine{
		{Name: "live", MachineID: "m-live", HostPubHex: "aa", SignalURL: "https://r", Owner: owner.Address},
		{Name: "dead", MachineID: "m-dead", HostPubHex: "bb", SignalURL: "https://r", Owner: owner.Address},
		{Name: "mine", MachineID: "m-mine", HostPubHex: "cc", SignalURL: "https://r"},
	} {
		if err := AddMachine(dir, m); err != nil {
			t.Fatal(err)
		}
	}

	SweepGuestState(dir, now)

	machines, _ := ListMachines(dir)
	names := map[string]bool{}
	for _, m := range machines {
		names[m.Name] = true
	}
	if !names["live"] || !names["mine"] || names["dead"] {
		t.Fatalf("sweep kept/removed the wrong entries: %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "grants", dead.GID+".json")); !os.IsNotExist(err) {
		t.Fatal("closed grant file survived the sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, "grants", liveSG.GID+".json")); err != nil {
		t.Fatal("live grant file was swept")
	}
}

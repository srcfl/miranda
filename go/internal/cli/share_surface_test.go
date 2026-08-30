// go/internal/cli/share_surface_test.go — G1d surface: share ls/revoke against
// the live harness, guest-flagged `mir ls` lines, the guest attach clock check,
// the overview's share row, and the expiry phrasing.
package cli

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestExpiryPhrase(t *testing.T) {
	now := time.Now()
	cases := []struct {
		na      int64
		revoked bool
		want    string
	}{
		{now.Add(30 * time.Second).Unix(), false, "expires in under a minute"},
		{now.Add(42 * time.Minute).Unix(), false, "expires in 4"}, // 41/42 min, rounding
		{now.Add(3*time.Hour + 10*time.Minute).Unix(), false, "expires in 3h"},
		{now.Add(-time.Minute).Unix(), false, "expired"},
		{now.Add(time.Hour).Unix(), true, "revoked"},
	}
	for _, tc := range cases {
		if got := expiryPhrase(tc.na, tc.revoked, now); !strings.HasPrefix(got, tc.want) {
			t.Errorf("expiryPhrase(na=%d, revoked=%v) = %q, want prefix %q", tc.na, tc.revoked, got, tc.want)
		}
	}
}

// TestShareLsRevokeLiveLoop extends the live harness through the full G1d loop:
// mint+join, `share ls` shows it, `share revoke <prefix>` lands on the agent,
// the local record flips, and a re-revoke says so.
func TestShareLsRevokeLiveLoop(t *testing.T) {
	h := startShareHarness(t)
	withShareTTY(t, true)
	deadline := time.Now().Add(40 * time.Second)

	shareOut := &safeBuf{}
	shareErr := make(chan error, 1)
	shareApp := &app{in: strings.NewReader("y\n"), out: shareOut, errOut: io.Discard, binary: "mir"}
	go func() {
		shareErr <- shareApp.cmdShare([]string{"--dir", h.ownerDir, "--web", "http://127.0.0.1", "sharebox"})
	}()
	joinCode := extractJoinCode(t, shareOut, shareErr, deadline)

	guestDir := t.TempDir()
	guestApp := &app{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, binary: "mir"}
	if err := guestApp.cmdJoin([]string{"--dir", guestDir, joinCode}); err != nil {
		t.Fatalf("join: %v\n%s", err, shareOut.String())
	}
	select {
	case err := <-shareErr:
		if err != nil {
			t.Fatalf("share: %v\n%s", err, shareOut.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("share did not finish:\n%s", shareOut.String())
	}

	// The guest's list flags the entry as a share, not a machine of their own.
	var guestLs bytes.Buffer
	lsApp := &app{in: strings.NewReader(""), out: &guestLs, errOut: io.Discard, binary: "mir"}
	if err := lsApp.cmdList([]string{"--dir", guestDir}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sharebox", "shared with you", "read-only", "expires in"} {
		if !strings.Contains(guestLs.String(), want) {
			t.Fatalf("guest ls missing %q:\n%s", want, guestLs.String())
		}
	}

	// share ls on the owner shows the mint with its short id.
	shares, err := client.ListOwnerShares(h.ownerDir)
	if err != nil || len(shares) != 1 {
		t.Fatalf("owner shares = %v (err %v)", shares, err)
	}
	gid := shares[0].Grant.GID
	var lsOut bytes.Buffer
	ownerLs := &app{in: strings.NewReader(""), out: &lsOut, errOut: io.Discard, binary: "mir"}
	if err := ownerLs.cmdShare([]string{"ls", "--dir", h.ownerDir}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{gid[:8], "sharebox", "read-only", "expires in"} {
		if !strings.Contains(lsOut.String(), want) {
			t.Fatalf("share ls missing %q:\n%s", want, lsOut.String())
		}
	}

	// Revoke by prefix against the live agent; the tombstone bars a re-attach.
	var revOut bytes.Buffer
	revApp := &app{in: strings.NewReader(""), out: &revOut, errOut: io.Discard, binary: "mir"}
	if err := revApp.cmdShare([]string{"revoke", "--dir", h.ownerDir, gid[:8]}); err != nil {
		t.Fatalf("revoke: %v\n%s", err, revOut.String())
	}
	if !strings.Contains(revOut.String(), "✓ revoked "+gid[:8]) {
		t.Fatalf("revoke copy:\n%s", revOut.String())
	}
	shares, _ = client.ListOwnerShares(h.ownerDir)
	if !shares[0].Revoked {
		t.Fatal("local record not marked revoked")
	}
	var againOut bytes.Buffer
	againApp := &app{in: strings.NewReader(""), out: &againOut, errOut: io.Discard, binary: "mir"}
	if err := againApp.cmdShare([]string{"revoke", "--dir", h.ownerDir, gid[:8]}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(againOut.String(), "already revoked") {
		t.Fatalf("re-revoke copy:\n%s", againOut.String())
	}
}

// TestGuestAttachExpiredShareRefusesLocally: an expired share is refused with
// the honest line before any network dial.
func TestGuestAttachExpiredShareRefuses(t *testing.T) {
	t.Setenv("MIR_TEST_KEYCHAIN_DIR", t.TempDir())
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	relay := httptest.NewServer(signal.New().Handler())
	defer relay.Close()
	t.Setenv("MIR_SIGNAL", relay.URL)

	dir := t.TempDir()
	guestID, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := offerCLISigner(t, 0x11)
	now := time.Now()
	dead, err := owner.SignGrant(identity.Grant{
		V: 1, Owner: owner.Address, Machine: "m-dead", Guest: guestID.OwnerID,
		Scope: "main", Mode: "ro", NB: now.Add(-2 * time.Hour).Unix(), NA: now.Add(-time.Hour).Unix(),
		GID: "deadbeefdeadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := dead.JSON()
	if err := client.SaveGuestGrant(dir, dead.GID, rec); err != nil {
		t.Fatal(err)
	}
	if err := client.AddMachine(dir, client.Machine{
		Name: "gone", MachineID: "m-dead", HostPubHex: "aa", SignalURL: relay.URL, Owner: owner.Address,
	}); err != nil {
		t.Fatal(err)
	}

	a := &app{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, binary: "mir"}
	err = a.cmdAttach([]string{"--dir", dir, "gone"})
	if err == nil || !strings.Contains(err.Error(), "share of \"gone\" has ended") {
		t.Fatalf("expected the honest expired-share refusal, got %v", err)
	}
}

func offerCLISigner(t *testing.T, fill byte) *identity.Signer {
	t.Helper()
	s, err := identity.DeriveSigner(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOverviewRenderSharedRow(t *testing.T) {
	m := &overviewModel{
		Binary: "mir",
		Rows: []overviewRow{
			{Name: "zap-dev", Online: true},
			{Name: "teambox", Shared: true, WindowsLine: "shared with you · read-only · expires in 42 min"},
		},
		Cursor: 1,
		Width:  100,
	}
	out := m.Render()
	for _, want := range []string{
		"▸ ⇢ teambox",
		"shared with you · read-only · expires in 42 min",
		"s share", // the hint bar carries the share action
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

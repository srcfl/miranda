// go/internal/cli/share_join_test.go — G1b acceptance: a live share on one
// machine (mint → join → the grant lands on the agent), and a declined safety
// number that pins nothing anywhere.
package cli

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func withShareTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prev := shareIsTTY
	shareIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { shareIsTTY = prev })
}

// extractJoinCode pulls the invite code from the printed "mir join <code>" line.
func extractJoinCode(t *testing.T, out *safeBuf, errCh <-chan error, deadline time.Time) string {
	t.Helper()
	for time.Now().Before(deadline) {
		if err, ended := tryEnded(errCh); ended {
			t.Fatalf("share ended before a code: %v\n%s", err, out.String())
		}
		s := out.String()
		if i := strings.Index(s, "mir join "); i >= 0 {
			if fields := strings.Fields(s[i+len("mir join "):]); len(fields) > 0 {
				return fields[0]
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no join code in output:\n%s", out.String())
	return ""
}

// shareHarness runs relay + agent + a paired owner, returning everything a
// share test needs. The agent serves `sh` over the hermetic relay.
type shareHarness struct {
	agentDir, ownerDir string
	agentOut           *safeBuf
	upErr              chan error
}

func startShareHarness(t *testing.T) *shareHarness {
	t.Helper()
	t.Setenv("MIR_TEST_KEYCHAIN_DIR", t.TempDir())
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	srv := httptest.NewServer(signal.New().Handler())
	t.Cleanup(srv.Close)
	t.Setenv("MIR_SIGNAL", srv.URL)

	h := &shareHarness{agentDir: t.TempDir(), ownerDir: t.TempDir(), agentOut: &safeBuf{}, upErr: make(chan error, 1)}
	agentApp := &app{binary: "mir", out: h.agentOut, errOut: io.Discard}
	go func() {
		h.upErr <- agentApp.cmdUp([]string{"--dir", h.agentDir, "--signal", srv.URL, "--web", "http://127.0.0.1",
			"--name", "sharebox", "--shell", "sh", "--no-lan", "--yes"})
	}()
	deadline := time.Now().Add(15 * time.Second)
	pairCode := extractPairCode(t, h.agentOut, h.upErr, deadline)

	var ownerOut bytes.Buffer
	ownerApp := &app{in: strings.NewReader(""), out: &ownerOut, errOut: io.Discard, binary: "mir"}
	if err := ownerApp.cmdPair([]string{"--dir", h.ownerDir, "--yes", pairCode}); err != nil {
		t.Fatalf("owner pair: %v\n%s", err, ownerOut.String())
	}
	waitFor(t, h.agentOut, h.upErr, "✓ paired", deadline)
	return h
}

func agentGrants(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "grants", "*.json"))
	return files
}

func TestShareJoinLiveGrantLandsOnAgent(t *testing.T) {
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
	var joinOut bytes.Buffer
	guestApp := &app{in: strings.NewReader(""), out: &joinOut, errOut: io.Discard, binary: "mir"}
	if err := guestApp.cmdJoin([]string{"--dir", guestDir, joinCode}); err != nil {
		t.Fatalf("join: %v\n%s\nshare out:\n%s", err, joinOut.String(), shareOut.String())
	}
	select {
	case err := <-shareErr:
		if err != nil {
			t.Fatalf("share: %v\n%s", err, shareOut.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("share did not finish:\n%s", shareOut.String())
	}

	// The grant landed on the agent, verifies, and names the guest identity.
	files := agentGrants(t, h.agentDir)
	if len(files) != 1 {
		t.Fatalf("agent grants = %v, want exactly one", files)
	}
	raw, _ := os.ReadFile(files[0])
	sg, err := identity.ParseSignedGrant(raw)
	if err != nil || identity.VerifyGrant(sg) != nil {
		t.Fatalf("agent-stored grant does not verify: %v", err)
	}
	guestID, err := client.LoadOrCreateIdentity(guestDir)
	if err != nil {
		t.Fatal(err)
	}
	if sg.Guest != guestID.OwnerID || sg.Mode != "ro" || sg.Scope != "main" {
		t.Fatalf("grant = %+v, want guest %s ro main", sg.Grant, guestID.OwnerID)
	}

	// The guest kept the machine and its copy of the grant.
	machines, err := client.ListMachines(guestDir)
	if err != nil || len(machines) != 1 || machines[0].Name != "sharebox" {
		t.Fatalf("guest machines = %+v (err %v)", machines, err)
	}
	if _, err := os.Stat(filepath.Join(guestDir, "grants", sg.GID+".json")); err != nil {
		t.Fatalf("guest grant copy missing: %v", err)
	}
	for _, want := range []string{"✓ shared", "read-only", "safety number"} {
		if !strings.Contains(shareOut.String(), want) {
			t.Fatalf("share copy missing %q:\n%s", want, shareOut.String())
		}
	}
	if !strings.Contains(joinOut.String(), "✓ joined") {
		t.Fatalf("join copy:\n%s", joinOut.String())
	}
}

func TestShareDeclinedSASPinsNothing(t *testing.T) {
	h := startShareHarness(t)
	withShareTTY(t, true)
	deadline := time.Now().Add(40 * time.Second)

	shareOut := &safeBuf{}
	shareErr := make(chan error, 1)
	shareApp := &app{in: strings.NewReader("n\n"), out: shareOut, errOut: io.Discard, binary: "mir"}
	go func() {
		shareErr <- shareApp.cmdShare([]string{"--dir", h.ownerDir, "--web", "http://127.0.0.1", "sharebox"})
	}()
	joinCode := extractJoinCode(t, shareOut, shareErr, deadline)

	guestDir := t.TempDir()
	guestApp := &app{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, binary: "mir"}
	joinErr := guestApp.cmdJoin([]string{"--dir", guestDir, joinCode})
	if joinErr == nil {
		t.Fatal("join must fail when the owner declines")
	}
	select {
	case err := <-shareErr:
		if err != nil {
			t.Fatalf("declining is a clean outcome, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("share did not finish after decline:\n%s", shareOut.String())
	}
	if !strings.Contains(shareOut.String(), "declined — nothing was shared") {
		t.Fatalf("decline copy:\n%s", shareOut.String())
	}
	if files := agentGrants(t, h.agentDir); len(files) != 0 {
		t.Fatalf("declined share reached the agent: %v", files)
	}
	if machines, _ := client.ListMachines(guestDir); len(machines) != 0 {
		t.Fatalf("declined share left the guest a machine: %+v", machines)
	}
	if files := agentGrants(t, guestDir); len(files) != 0 {
		t.Fatalf("declined share left the guest a grant: %v", files)
	}
}

func TestShareRefusesNonTTY(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	withShareTTY(t, false)
	a := &app{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, binary: "mir"}
	err := a.cmdShare([]string{"--dir", t.TempDir(), "box"})
	if err == nil || !strings.Contains(err.Error(), "needs a person") {
		t.Fatalf("expected the interactive refusal, got %v", err)
	}
}

func TestShareWriteConsentNameMismatchCancels(t *testing.T) {
	t.Setenv("MIR_TEST_KEYCHAIN_DIR", t.TempDir())
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	relay := httptest.NewServer(nil) // never reached before the consent gate fails
	relay.Close()
	t.Setenv("MIR_SIGNAL", relay.URL)
	withShareTTY(t, true)

	dir := t.TempDir()
	if _, err := client.LoadOrCreateIdentity(dir); err != nil {
		t.Fatal(err)
	}
	if err := client.AddMachine(dir, client.Machine{Name: "box", MachineID: "machine-1", HostPubHex: "aabb", SignalURL: relay.URL}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &app{in: strings.NewReader("not-box\n"), out: &out, errOut: io.Discard, binary: "mir"}
	if err := a.cmdShare([]string{"--dir", dir, "--write", "box"}); err != nil {
		t.Fatalf("consent mismatch is a clean cancel, got %v", err)
	}
	if !strings.Contains(out.String(), "cancelled") || strings.Contains(out.String(), "safety number") {
		t.Fatalf("write consent must cancel before any invite exists:\n%s", out.String())
	}
}

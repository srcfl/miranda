package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/version"
)

// TestIsCleanDetach: a normal peer disconnect (data channel closed) or io.EOF is a
// clean exit (nil-worthy); a real failure is not. This is what lets single-machine
// `attach` stop printing "error: …"/exit 1 on an ordinary agent detach.
func TestIsCleanDetach(t *testing.T) {
	if !isCleanDetach(peer.ErrDataChannelClosed) {
		t.Error("ErrDataChannelClosed should be a clean detach")
	}
	if !isCleanDetach(fmt.Errorf("attach %s: %w", "box", peer.ErrDataChannelClosed)) {
		t.Error("wrapped ErrDataChannelClosed should be a clean detach")
	}
	if !isCleanDetach(io.EOF) {
		t.Error("io.EOF should be a clean detach")
	}
	if isCleanDetach(errors.New("dial timeout")) {
		t.Error("an arbitrary error must not be treated as a clean detach")
	}
	if isCleanDetach(nil) {
		t.Error("nil is not a detach error")
	}
}

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "mir ") || !strings.Contains(out.String(), version.Version) {
		t.Fatalf("version output = %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"wat"}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", errb.String())
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

// No-args prints the getting-started guide (not just a terse usage line).
func TestNoArgsShowsGuide(t *testing.T) {
	var out, errb bytes.Buffer
	Run(nil, &out, &errb)
	g := out.String()
	for _, want := range []string{"mir attach", "mir pair", "wallet", "LAN"} {
		if !strings.Contains(g, want) {
			t.Fatalf("guide missing %q:\n%s", want, g)
		}
	}
}

// A legacy (pre-wallet) identity attaching is guided to `mir keygen --wallet`
// rather than failing with a cryptic handshake/usage error.
func TestAttachLegacyIdentityGuidesToKeygen(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	legacy := `{"owner_priv":"` + strings.Repeat("aa", 32) + `","owner_pub":"` + strings.Repeat("bb", 32) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"attach", "--dir", dir, "box"}, &out, &errb); code == 0 {
		t.Fatal("attach with a wallet-less identity should fail")
	}
	if !strings.Contains(errb.String(), "keygen --wallet") || !strings.Contains(errb.String(), "re-paired") {
		t.Fatalf("expected a keygen + re-pair migration hint, got:\n%s", errb.String())
	}
}

func TestKeygenPrintsOwnerKey(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"keygen", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "owner public key") {
		t.Fatalf("keygen output = %q", out.String())
	}
}

func TestListEmptyThenAddMachine(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	// list now fetches the wallet registry on the default relay; point it at a dead
	// local address so the unit test stays hermetic (FetchRegistry fails fast and is
	// best-effort, so list still falls back to the local machines.json).
	t.Setenv("MIR_SIGNAL", "http://127.0.0.1:1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "no machines yet") {
		t.Fatalf("empty list = %q", out.String())
	}
	out.Reset()
	add := []string{"add-machine", "--dir", dir, "--name", "box", "--id", "m1",
		"--host-pub", "aabbcc", "--signal", "https://relay.example"}
	if code := Run(add, &out, &errb); code != 0 {
		t.Fatalf("add exit = %d, stderr = %q", code, errb.String())
	}
	out.Reset()
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list2 exit = %d", code)
	}
	if !strings.Contains(out.String(), "box") || !strings.Contains(out.String(), "m1") {
		t.Fatalf("list after add = %q", out.String())
	}
}

func TestEnrollPrintsMachineID(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := Run([]string{"enroll", "--dir", dir, "--name", "testbox", "--signal", "https://relay.example"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "machine_id:") || !strings.Contains(out.String(), "testbox") {
		t.Fatalf("enroll output = %q", out.String())
	}
}

func TestPairDevPinsOwner(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"enroll", "--dir", dir, "--name", "b", "--signal", "https://relay.example"}, &out, &errb); code != 0 {
		t.Fatalf("enroll exit = %d", code)
	}
	out.Reset()
	owner := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if code := Run([]string{"pair-dev", "--dir", dir, "--owner-pub", owner}, &out, &errb); code != 0 {
		t.Fatalf("pair-dev exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "pinned owner") {
		t.Fatalf("pair-dev output = %q", out.String())
	}
}

func TestRunAgentCompatWarnsAndForwards(t *testing.T) {
	var out, errb bytes.Buffer
	if code := RunAgentCompat([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(strings.ToLower(errb.String()), "deprecated") {
		t.Fatalf("stderr = %q, want deprecation notice", errb.String())
	}
	if !strings.HasPrefix(out.String(), "mir-agent ") {
		t.Fatalf("stdout = %q, want mir-agent version label", out.String())
	}
}

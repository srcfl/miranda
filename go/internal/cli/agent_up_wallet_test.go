package cli

import (
	"bytes"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/agent"
)

// TestUpAutoPinsOwnWallet proves the wallet block in `mir up` auto-pins the
// machine's own wallet as a served owner (so your own devices need no pairing)
// and hands the wallet secret to the Runtime so it can publish the registry
// record. A wallet-rooted identity must end up pinned; the Runtime must carry the
// wallet.
func TestUpAutoPinsOwnWallet(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	a := &app{out: &out, errOut: &errb, binary: "mir"}

	// Enroll the agent so config.json exists (PinOwner reads/writes it).
	cfg, err := agent.LoadOrInit(dir, "test-machine", "https://relay.example")
	if err != nil {
		t.Fatalf("LoadOrInit: %v", err)
	}
	rt := agent.NewRuntime(cfg, []string{"sh"}, nil)

	// The unit under test: load the wallet, auto-pin it, wire it into the Runtime.
	if err := a.applyWalletToUp(dir, rt); err != nil {
		t.Fatalf("applyWalletToUp: %v", err)
	}

	idn, err := a.identity(dir)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if !idn.HasWallet() {
		t.Fatal("fresh identity should be wallet-rooted")
	}
	if pinned, err := agent.ReloadOwners(dir); err != nil {
		t.Fatalf("ReloadOwners: %v", err)
	} else if !contains(pinned, idn.WalletAddress) {
		t.Fatalf("own wallet %s not pinned; owners = %v", idn.WalletAddress, pinned)
	}
	if rt.WalletAddress != idn.WalletAddress {
		t.Fatalf("rt.WalletAddress = %q, want %q", rt.WalletAddress, idn.WalletAddress)
	}
	if len(rt.WalletSecret) == 0 {
		t.Fatal("rt.WalletSecret not set; Runtime cannot publish the registry record")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

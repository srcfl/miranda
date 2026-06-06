// go/internal/agent/store_test.go
package agent

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreInitPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()

	cfg, err := LoadOrInit(dir, "macbook", "http://localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostPriv()) != 32 || len(cfg.HostPub()) != 32 {
		t.Fatalf("host key not initialized: priv=%d pub=%d", len(cfg.HostPriv()), len(cfg.HostPub()))
	}
	if cfg.MachineID == "" {
		t.Fatal("machine id not generated")
	}
	secret, err := hex.DecodeString(cfg.RegistrationSecret)
	if err != nil || len(secret) != 32 {
		t.Fatalf("registration secret not initialized: len=%d err=%v", len(secret), err)
	}

	// Reload: same host key + machine id (stable identity).
	cfg2, err := LoadOrInit(dir, "macbook", "http://localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.MachineID != cfg.MachineID || cfg2.HostPrivHex != cfg.HostPrivHex {
		t.Fatal("identity not stable across reloads")
	}
	if cfg2.RegistrationSecret != cfg.RegistrationSecret {
		t.Fatal("registration secret not stable across reloads")
	}
}

// TestStoreTightensLoosePreExistingPerms verifies that a pre-existing config dir
// and config.json with loose (group/world-readable) permissions are hardened on
// load/save. The host private key in config.json must never stay world-readable.
func TestStoreTightensLoosePreExistingPerms(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a loose dir and a loose config.json (simulating an older agent,
	// a restored backup that dropped mode bits, or a manual edit).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrInit(dir, "m", "http://localhost:8443"); err != nil {
		t.Fatal(err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir perms = %o, want 0700", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config.json perms = %o, want 0600 (host private key must not be group/world readable)", perm)
	}

	// PinOwner also re-saves; it must keep perms tight too.
	if err := PinOwner(dir, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(path)
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config.json perms after PinOwner = %o, want 0600", perm)
	}
}

func TestPinOwnerPersists(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := LoadOrInit(dir, "m", "http://localhost:8443")
	if cfg.IsOwnerPinned("deadbeef") {
		t.Fatal("owner should not be pinned yet")
	}
	if err := PinOwner(dir, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadOrInit(dir, "m", "http://localhost:8443")
	if !reloaded.IsOwnerPinned("deadbeef") {
		t.Fatal("pinned owner did not persist")
	}
}

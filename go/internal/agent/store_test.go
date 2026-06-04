// go/internal/agent/store_test.go
package agent

import (
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

	// Reload: same host key + machine id (stable identity).
	cfg2, err := LoadOrInit(dir, "macbook", "http://localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.MachineID != cfg.MachineID || cfg2.HostPrivHex != cfg.HostPrivHex {
		t.Fatal("identity not stable across reloads")
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

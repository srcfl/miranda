// go/internal/client/store_test.go
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityIsCreatedOnceAndStable(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.OwnerPriv()) != 32 || len(id.OwnerPub()) != 32 {
		t.Fatalf("owner key not initialized: priv=%d pub=%d", len(id.OwnerPriv()), len(id.OwnerPub()))
	}
	id2, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id2.OwnerPrivHex != "" {
		t.Fatalf("derived owner private key was persisted: %q", id2.OwnerPrivHex)
	}
	if id2.SecretHex != "" || id2.SecretRef == "" {
		t.Fatalf("root was not migrated to keychain metadata: secret=%q ref=%q", id2.SecretHex, id2.SecretRef)
	}
	backend, err := id2.CheckSecretStorage()
	if err != nil || backend != "test keychain" {
		t.Fatalf("CheckSecretStorage = %q, %v", backend, err)
	}
	data, err := os.ReadFile(identityPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["secret"]; ok {
		t.Fatal("owner.json contains the root secret")
	}
	if _, ok := raw["owner_priv"]; ok {
		t.Fatal("owner.json contains a derived private key")
	}
	if !bytes.Equal(id2.OwnerPriv(), id.OwnerPriv()) {
		t.Fatal("owner identity not stable across re-derivation")
	}
}

func TestLegacyPlaintextRootMigratesAtomicallyToKeychain(t *testing.T) {
	dir := t.TempDir()
	root := bytes.Repeat([]byte{0x31}, 32)
	legacy := map[string]string{"secret": fmt.Sprintf("%x", root), "owner_priv": strings.Repeat("aa", 32)}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(identityPath(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(id.Secret(), root) || id.SecretRef == "" {
		t.Fatal("migrated identity lost its root")
	}
	after, err := os.ReadFile(identityPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(after, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["secret"]; ok {
		t.Fatal("plaintext secret survived migration")
	}
	if _, ok := raw["owner_priv"]; ok {
		t.Fatal("derived private key survived migration")
	}
}

func TestMissingKeychainEntryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := platformSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(id.SecretRef); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(dir); err == nil {
		t.Fatal("missing keychain entry unexpectedly created a new identity")
	}
	// The metadata remains present; fail-closed must not rewrite it.
	if _, err := os.Stat(filepath.Join(dir, "owner.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAddAndGetMachine(t *testing.T) {
	dir := t.TempDir()
	m := Machine{Name: "macbook", MachineID: "abc123", HostPubHex: "deadbeef", SignalURL: "http://localhost:8443"}
	if err := AddMachine(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := GetMachine(dir, "macbook")
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != "abc123" || got.HostPubHex != "deadbeef" {
		t.Fatalf("machine mismatch: %+v", got)
	}
	// Re-adding the same name updates in place (no duplicate).
	m.HostPubHex = "cafe"
	if err := AddMachine(dir, m); err != nil {
		t.Fatal(err)
	}
	list, _ := ListMachines(dir)
	if len(list) != 1 || list[0].HostPubHex != "cafe" {
		t.Fatalf("expected 1 updated machine, got %+v", list)
	}
}

func TestGetMissingMachineErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := GetMachine(dir, "nope"); err == nil {
		t.Fatal("expected error for unknown machine")
	}
}

// TestAddMachineDoesNotDestroyCorruptStore guards the pin set: if machines.json
// is unreadable/corrupt (e.g. a truncated write from a prior crash), AddMachine
// must NOT silently overwrite the whole file with just the new entry. The pinned
// host pubkeys anchor the Noise KK trust decision, so silent loss is unacceptable.
func TestAddMachineDoesNotDestroyCorruptStore(t *testing.T) {
	dir := t.TempDir()
	// Simulate a truncated/partial write: valid JSON prefix, cut off mid-array.
	corrupt := []byte(`[{"name":"a","host_pub":"aa"},{"name":"b","host_pub":"bb"},{"name":"c"`)
	if err := os.WriteFile(machinesPath(dir), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	m := Machine{Name: "d", HostPubHex: "dd"}
	if err := AddMachine(dir, m); err == nil {
		t.Fatal("expected AddMachine to return an error on corrupt store, got nil")
	}
	// The corrupt file must be left untouched, not overwritten with [d].
	after, err := os.ReadFile(machinesPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(corrupt) {
		t.Fatalf("AddMachine overwrote corrupt store; pin set lost. file=%s", after)
	}
}

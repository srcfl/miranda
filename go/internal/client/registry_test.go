package client

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// sealEntry builds the relay's {machine_id, blob} wire entry for a record sealed
// under the given secret's K_reg. This mirrors what a wallet-holding agent does.
func sealEntry(t *testing.T, secret []byte, machineID, name, hostPub, signalURL string) struct {
	MachineID string `json:"machine_id"`
	Blob      string `json:"blob"`
} {
	t.Helper()
	key, err := identity.RegistryKey(secret)
	if err != nil {
		t.Fatalf("RegistryKey: %v", err)
	}
	rec := map[string]any{
		"v":          1,
		"name":       name,
		"host_pub":   hostPub,
		"signal_url": signalURL,
		"ts":         1749600000,
	}
	pt, _ := json.Marshal(rec)
	nonce := make([]byte, 12) // fixed nonce fine for a test
	blob, err := identity.SealRecord(key, nonce, pt, machineID)
	if err != nil {
		t.Fatalf("SealRecord: %v", err)
	}
	return struct {
		MachineID string `json:"machine_id"`
		Blob      string `json:"blob"`
	}{MachineID: machineID, Blob: base64.StdEncoding.EncodeToString(blob)}
}

// walletIdentity builds a prf-rooted identity from a fixed secret so the test can
// derive the same K_reg the fake relay sealed under.
func walletIdentity(t *testing.T, secretHex string) *Identity {
	t.Helper()
	id := &Identity{}
	secret := mustHex(t, secretHex)
	if err := id.SetFromSecret(secret); err != nil {
		t.Fatalf("SetFromSecret: %v", err)
	}
	return id
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

const testSecretHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// TestFetchRegistryDecryptsOwnRecords: a record sealed under the same wallet
// secret is fetched, opened, and returned as a Machine. A second entry sealed
// under a DIFFERENT key fails to open and is silently dropped.
func TestFetchRegistryDecryptsAndDropsForgeries(t *testing.T) {
	id := walletIdentity(t, testSecretHex)

	good := sealEntry(t, id.Secret(), "m-good", "kitchen", "aa11bb22", "wss://relay.example/agent")
	// Forged: sealed under a different secret -> opens to garbage under our K_reg.
	forged := sealEntry(t, mustHex(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		"m-forged", "evil", "deadbeef", "wss://evil.example/agent")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("wallet"); got != id.WalletAddress {
			t.Errorf("wallet query = %q, want %q", got, id.WalletAddress)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{good, forged})
	}))
	defer srv.Close()

	got, err := FetchRegistry(context.Background(), nil, srv.URL, id)
	if err != nil {
		t.Fatalf("FetchRegistry: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d machines, want 1 (forgery dropped): %+v", len(got), got)
	}
	m := got[0]
	if m.Name != "kitchen" || m.MachineID != "m-good" || m.HostPubHex != "aa11bb22" {
		t.Fatalf("decoded machine = %+v", m)
	}
	if m.SignalURL != "wss://relay.example/agent" {
		t.Fatalf("signal_url = %q", m.SignalURL)
	}
}

// A record whose sealed payload omits signal_url inherits the fetch signalURL,
// so the returned Machine is always attachable.
func TestFetchRegistryFallsBackToFetchSignalURL(t *testing.T) {
	id := walletIdentity(t, testSecretHex)
	e := sealEntry(t, id.Secret(), "m1", "box", "cc33", "") // empty signal_url in record

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{e})
	}))
	defer srv.Close()

	got, err := FetchRegistry(context.Background(), nil, srv.URL, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SignalURL != srv.URL {
		t.Fatalf("expected fallback signal_url=%q, got %+v", srv.URL, got)
	}
}

// A wallet-less (legacy) identity has no K_reg, so FetchRegistry is a no-op (nil,
// nil) — it never even hits the relay.
func TestFetchRegistryWalletlessIsNil(t *testing.T) {
	legacy := &Identity{OwnerPrivHex: strings.Repeat("aa", 32), OwnerPubHex: strings.Repeat("bb", 32)}
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	got, err := FetchRegistry(context.Background(), nil, srv.URL, legacy)
	if err != nil {
		t.Fatalf("wallet-less FetchRegistry should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("wallet-less FetchRegistry should return nil, got %+v", got)
	}
	if hit {
		t.Fatal("wallet-less FetchRegistry must not contact the relay")
	}
}

// A relay error (non-200, or unreachable) is best-effort: FetchRegistry returns
// an error the caller ignores, never a panic, and never a partial list.
func TestFetchRegistryRelayErrorIsBestEffort(t *testing.T) {
	id := walletIdentity(t, testSecretHex)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got, err := FetchRegistry(context.Background(), nil, srv.URL, id)
	if err == nil {
		t.Fatal("expected an error on a 500 relay")
	}
	if got != nil {
		t.Fatalf("expected nil machines on relay error, got %+v", got)
	}
}

func TestMergeMachines(t *testing.T) {
	local := []Machine{
		{Name: "box", MachineID: "m1", HostPubHex: "local11", SignalURL: "wss://local"},
		{Name: "laptop", MachineID: "m2", HostPubHex: "local22", SignalURL: "wss://local"},
	}
	discovered := []Machine{
		{Name: "box-renamed", MachineID: "m1", HostPubHex: "disc11", SignalURL: "wss://disc"}, // same id -> local wins
		{Name: "kitchen", MachineID: "m3", HostPubHex: "disc33", SignalURL: "wss://disc"},     // new -> added
	}

	merged := MergeMachines(local, discovered)
	if len(merged) != 3 {
		t.Fatalf("merged len = %d, want 3: %+v", len(merged), merged)
	}
	byID := map[string]Machine{}
	for _, m := range merged {
		byID[m.MachineID] = m
	}
	// Local wins for m1.
	if byID["m1"].Name != "box" || byID["m1"].HostPubHex != "local11" {
		t.Fatalf("m1 should keep the local entry, got %+v", byID["m1"])
	}
	// Discovered-only added.
	if byID["m3"].Name != "kitchen" || byID["m3"].HostPubHex != "disc33" {
		t.Fatalf("m3 (discovered) should be added, got %+v", byID["m3"])
	}
	// Local-only preserved.
	if byID["m2"].Name != "laptop" {
		t.Fatalf("m2 (local-only) should be preserved, got %+v", byID["m2"])
	}
}

func TestNotifyNewDevices(t *testing.T) {
	dir := t.TempDir()
	machines := []Machine{
		{Name: "box", MachineID: "m1"},
		{Name: "kitchen", MachineID: "m2"},
	}

	var b strings.Builder
	if err := NotifyNewDevices(&b, dir, machines); err != nil {
		t.Fatalf("NotifyNewDevices: %v", err)
	}
	first := b.String()
	if !strings.Contains(first, `"box"`) || !strings.Contains(first, `"kitchen"`) {
		t.Fatalf("first notify should name both new devices, got:\n%s", first)
	}
	if !strings.Contains(first, "new device") || !strings.Contains(first, "joined your wallet") {
		t.Fatalf("first notify wording = %q", first)
	}

	// seen.json must now contain both ids.
	if _, err := os.Stat(filepath.Join(dir, "seen.json")); err != nil {
		t.Fatalf("seen.json not persisted: %v", err)
	}

	// Second call with the same ids prints nothing.
	b.Reset()
	if err := NotifyNewDevices(&b, dir, machines); err != nil {
		t.Fatalf("NotifyNewDevices (2nd): %v", err)
	}
	if b.String() != "" {
		t.Fatalf("second notify should be silent, got:\n%s", b.String())
	}

	// A genuinely new id fires once.
	b.Reset()
	machines = append(machines, Machine{Name: "garage", MachineID: "m3"})
	if err := NotifyNewDevices(&b, dir, machines); err != nil {
		t.Fatalf("NotifyNewDevices (3rd): %v", err)
	}
	if !strings.Contains(b.String(), `"garage"`) {
		t.Fatalf("new device should fire, got:\n%s", b.String())
	}
	if strings.Contains(b.String(), `"box"`) || strings.Contains(b.String(), `"kitchen"`) {
		t.Fatalf("already-seen devices should stay silent, got:\n%s", b.String())
	}
}

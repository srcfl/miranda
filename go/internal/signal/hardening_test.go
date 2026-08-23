package signal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// waitAgentCount polls the live-agent map until it reaches want, or fails.
func waitAgentCount(t *testing.T, s *Server, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		n := len(s.agents)
		s.mu.Unlock()
		if n == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	n := len(s.agents)
	s.mu.Unlock()
	t.Fatalf("agent count = %d, want %d", n, want)
}

// TestAgentCapBoundsNewRegistrations is the regression guard for the unbounded
// agent-registration memory DoS: without a cap, an attacker opens a registration
// per (owner, machine) and holds each socket (with a retained registry blob).
// New slots must be refused past the cap, while replacing an existing slot
// (routine on restart) must still succeed.
func TestAgentCapBoundsNewRegistrations(t *testing.T) {
	s := New()
	s.SetMaxAgents(2)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	a0 := registerAgentWithRegistry(t, srv.URL, "o", "m0", "blob0")
	defer a0.CloseNow()
	a1 := registerAgentWithRegistry(t, srv.URL, "o", "m1", "blob1")
	defer a1.CloseNow()
	waitAgentCount(t, s, 2)

	// A brand-new third slot is refused: the upgrade succeeds but the relay closes
	// the socket before sending Ready, and the map never grows past the cap.
	third := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m2"}))
	defer third.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := third.Read(ctx); err == nil {
		t.Fatal("third registration past the cap should be refused, but the socket stayed open")
	}
	s.mu.Lock()
	_, m2Live := s.agents[key("o", "m2")]
	n := len(s.agents)
	s.mu.Unlock()
	if m2Live {
		t.Fatal("m2 was admitted despite the cap")
	}
	if n != 2 {
		t.Fatalf("agent count = %d, want 2 (cap holds)", n)
	}

	// Replacing an already-registered slot must still work at the cap — it does
	// not grow the map.
	replace := registerAgentWithRegistry(t, srv.URL, "o", "m0", "blob0b")
	defer replace.CloseNow()
	waitAgentCount(t, s, 2)
}

// TestOversizedRegistryBlobDropped guards the per-agent registry memory bound:
// a blob larger than maxRegistryBlobBytes (but under the 256 KiB signal limit) is
// dropped rather than retained, removing the memory-amplification an attacker
// would otherwise get.
func TestOversizedRegistryBlobDropped(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	oversized := strings.Repeat("A", maxRegistryBlobBytes+1)
	a := registerAgentWithRegistry(t, srv.URL, "wallet-Z", "big", oversized)
	defer a.CloseNow()
	// Give the reader a moment to process (and drop) the blob.
	time.Sleep(200 * time.Millisecond)
	entries := getRegistry(t, srv.URL, "wallet-Z", http.StatusOK)
	if len(entries) != 0 {
		t.Fatalf("oversized blob was retained: %+v", entries)
	}

	// Control: a normally-sized blob under the same owner is still served.
	small := registerAgentWithRegistry(t, srv.URL, "wallet-Z", "small", "tiny-blob")
	defer small.CloseNow()
	time.Sleep(200 * time.Millisecond)
	entries = getRegistry(t, srv.URL, "wallet-Z", http.StatusOK)
	if len(entries) != 1 || entries[0].MachineID != "small" {
		t.Fatalf("small blob not served alongside dropped oversized blob: %+v", entries)
	}
}

// TestDuplicateRevocationSkipsPersist proves the fsync is skipped for an exact
// replay: after the first POST persists the file, the store directory is made
// unwritable so any real persist would fail with 500. A byte-identical replay
// must still return 204 — which is only possible if it never touches disk.
func TestRevocationPersistFailureRollsBackAndDoesNot204(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revocations.json")
	s := New()
	if err := s.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	record := signedRevocation(t, bytes.Repeat([]byte{0x72}, 32), "machine-fail")

	resp := postRevocation(t, srv.URL, record)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first persist failure status = %d, want 500", resp.StatusCode)
	}
	dup := postRevocation(t, srv.URL, record)
	io.Copy(io.Discard, dup.Body)
	dup.Body.Close()
	if dup.StatusCode == http.StatusNoContent {
		t.Fatal("retry after failed persist must not 204-skip; tombstone was never durable")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reloaded := New()
	if err := reloaded.LoadRevocations(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded.mu.Lock()
	_, ok := reloaded.revoked[key(record.OwnerID, record.MachineID)]
	reloaded.mu.Unlock()
	if ok {
		t.Fatal("tombstone must be absent after restart when persist never succeeded")
	}
}

func TestLoadedRevocationIsDurableAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revocations.json")
	s := New()
	if err := s.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	record := signedRevocation(t, bytes.Repeat([]byte{0x73}, 32), "machine-restart")
	resp := postRevocation(t, srv.URL, record)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first POST status = %d, want 204", resp.StatusCode)
	}

	reloaded := New()
	if err := reloaded.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	rsrv := httptest.NewServer(reloaded.Handler())
	defer rsrv.Close()
	dup := postRevocation(t, rsrv.URL, record)
	io.Copy(io.Discard, dup.Body)
	dup.Body.Close()
	if dup.StatusCode != http.StatusNoContent {
		t.Fatalf("POST after restart status = %d, want 204 (tombstone is already on disk)", dup.StatusCode)
	}
}

func TestDuplicateRevocationSkipsPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revocations.json")
	s := New()
	if err := s.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	record := signedRevocation(t, bytes.Repeat([]byte{0x71}, 32), "machine-dup")

	resp := postRevocation(t, srv.URL, record)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first POST status = %d, want 204", resp.StatusCode)
	}

	// Make the store dir unwritable: a real persist (CreateTemp + rename) would now
	// fail. Restore perms on cleanup so t.TempDir removal succeeds.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)

	dup := postRevocation(t, srv.URL, record)
	io.Copy(io.Discard, dup.Body)
	dup.Body.Close()
	if dup.StatusCode != http.StatusNoContent {
		t.Fatalf("duplicate POST status = %d, want 204 (persist must be skipped, not attempted)", dup.StatusCode)
	}
}

// TestConcurrentRevocationsAllPersist exercises the lock-free, versioned
// persister: many distinct revocations posted concurrently must all survive to
// disk. The stale-snapshot skip is only safe if a higher version is always a
// superset — this asserts no write is lost.
func TestConcurrentRevocationsAllPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revocations.json")
	s := New()
	if err := s.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	const n = 12
	records := make([]struct {
		root    []byte
		machine string
	}, n)
	for i := range records {
		records[i].root = bytes.Repeat([]byte{byte(0x40 + i)}, 32)
		records[i].machine = "machine-conc-" + string(rune('a'+i))
	}

	var wg sync.WaitGroup
	for i := range records {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := signedRevocation(t, records[i].root, records[i].machine)
			resp := postRevocation(t, srv.URL, rec)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("POST %d status = %d, want 204", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()

	// Reload from disk in a fresh server: every concurrently-posted tombstone must
	// be present and signature-valid.
	reloaded := New()
	if err := reloaded.LoadRevocations(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded.mu.Lock()
	got := len(reloaded.revoked)
	reloaded.mu.Unlock()
	if got != n {
		t.Fatalf("persisted %d revocations, want %d (a concurrent write was lost)", got, n)
	}
}

// TestRegistryAndRevocationsNoStore ensures a caching intermediary cannot serve a
// stale registry or revocation list (a stale empty revocation list could let a
// client attach to a revoked machine).
func TestRegistryAndRevocationsNoStore(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	for _, u := range []string{"/registry?owner_id=w", "/revocations?owner_id=w"} {
		resp, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		got := resp.Header.Get("Cache-Control")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", u, got)
		}
	}
}

// TestAgentRegistrationRejectsPipeInIdentifiers guards the owner|machine slot
// namespace: '|' in an identifier must be refused so an attacker cannot inject a
// bogus entry into another owner's registry listing.
func TestAgentRegistrationRejectsPipeInIdentifiers(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "victim|evil", "machine_id": "m"}), nil)
	if err == nil {
		t.Fatal("expected the dial to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want 400", resp)
	}
}

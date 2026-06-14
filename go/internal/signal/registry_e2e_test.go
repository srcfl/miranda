package signal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// TestRegistryE2ESealedRecordRoundTrips ties the registry contract together over a
// real relay: an agent seals a real record (B2.0) + base64s it onto its live
// registration (B2.2 wire), the relay serves it verbatim (B2.1, blind), and the
// fetcher base64-decodes + OpenRecords it with machine_id as AAD (B2.3 wire) to
// recover the record. This catches cross-component contract drift (base64 form,
// JSON field names, the AAD) that the per-slice fixtures cannot.
func TestRegistryE2ESealedRecordRoundTrips(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	secret := bytes.Repeat([]byte{0x07}, 32)
	key, err := identity.RegistryKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := identity.DeriveWallet(secret)
	if err != nil {
		t.Fatal(err)
	}
	const machineID = "m-laptop-1"
	rec := map[string]any{
		"v": 1, "name": "laptop", "host_pub": strings.Repeat("ab", 32),
		"signal_url": "https://relay.example", "ts": 1749600000,
	}
	pt, _ := json.Marshal(rec)
	nonce := bytes.Repeat([]byte{0x01}, 12)
	blob, err := identity.SealRecord(key, nonce, pt, machineID)
	if err != nil {
		t.Fatal(err)
	}
	b64blob := base64.StdEncoding.EncodeToString(blob)

	a := registerAgentWithRegistry(t, srv.URL, wallet.Address, machineID, b64blob)
	defer a.CloseNow()
	// A second agent under the SAME wallet publishing a FORGED blob (sealed under a
	// different key) must be served by the (blind) relay but dropped by the fetcher.
	forgedKey, _ := identity.RegistryKey(bytes.Repeat([]byte{0xff}, 32))
	forged, _ := identity.SealRecord(forgedKey, nonce, pt, "m-forged")
	f := registerAgentWithRegistry(t, srv.URL, wallet.Address, "m-forged", base64.StdEncoding.EncodeToString(forged))
	defer f.CloseNow()

	// Poll until both blobs are live on the relay.
	var entries []registryEntry
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries = getRegistry(t, srv.URL, wallet.Address, http.StatusOK)
		if len(entries) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(entries) != 2 {
		t.Fatalf("relay served %d entries, want 2", len(entries))
	}

	// Fetch + decode exactly as the client does: base64-decode, OpenRecord with the
	// machine_id as AAD. The real record opens; the forgery is dropped.
	opened := 0
	for _, e := range entries {
		raw, err := base64.StdEncoding.DecodeString(e.Blob)
		if err != nil {
			t.Fatalf("relay blob is not base64: %v", err)
		}
		decPt, err := identity.OpenRecord(key, raw, e.MachineID)
		if err != nil {
			continue // forgery / wrong wallet — dropped, exactly like client.FetchRegistry
		}
		opened++
		var got map[string]any
		if err := json.Unmarshal(decPt, &got); err != nil {
			t.Fatalf("opened record is not JSON: %v", err)
		}
		if e.MachineID != machineID || got["name"] != "laptop" || got["host_pub"] != strings.Repeat("ab", 32) {
			t.Fatalf("opened record mismatch: id=%s rec=%v", e.MachineID, got)
		}
	}
	if opened != 1 {
		t.Fatalf("opened %d records, want exactly 1 (the forgery must be dropped)", opened)
	}

	// Sanity: the relay never persisted anything — a fresh Server has no registry.
	fresh := httptest.NewServer(New().Handler())
	defer fresh.Close()
	if got := getRegistry(t, fresh.URL, wallet.Address, http.StatusOK); len(got) != 0 {
		t.Fatalf("a fresh relay must serve an empty registry, got %+v", got)
	}
}

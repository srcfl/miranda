package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

func testRevocation(t *testing.T, machine string) identity.Revocation {
	t.Helper()
	signer, err := identity.DeriveSigner(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	record, err := signer.SignRevocation(machine, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return *record
}

func TestRecordAndFilterRevocations(t *testing.T) {
	dir := t.TempDir()
	record := testRevocation(t, "machine-1")
	if err := RecordRevocation(dir, record); err != nil {
		t.Fatal(err)
	}
	// Duplicate recording is idempotent.
	if err := RecordRevocation(dir, record); err != nil {
		t.Fatal(err)
	}
	records, err := ListRevocations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !IsMachineRevoked("machine-1", record.OwnerID, records) {
		t.Fatalf("records = %+v", records)
	}
	filtered := FilterRevoked([]Machine{{MachineID: "machine-1"}, {MachineID: "machine-2"}}, record.OwnerID, records)
	if len(filtered) != 1 || filtered[0].MachineID != "machine-2" {
		t.Fatalf("filtered = %+v", filtered)
	}
	info, err := os.Stat(filepath.Join(dir, "revocations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestListRevocationsFailsClosedOnTampering(t *testing.T) {
	dir := t.TempDir()
	record := testRevocation(t, "machine-1")
	if err := RecordRevocation(dir, record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "revocations.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("machine-1"), []byte("machine-2"), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRevocations(dir); err == nil {
		t.Fatal("tampered local store unexpectedly accepted")
	}
}

func TestFetchAndPostRevocationsVerifyRelayData(t *testing.T) {
	record := testRevocation(t, "machine-1")
	var posted identity.Revocation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("owner_id") != record.OwnerID {
				t.Fatalf("owner query = %q", r.URL.Query().Get("owner_id"))
			}
			json.NewEncoder(w).Encode([]identity.Revocation{record})
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	records, err := FetchRevocations(context.Background(), nil, server.URL, record.OwnerID)
	if err != nil || len(records) != 1 {
		t.Fatalf("FetchRevocations = %+v, %v", records, err)
	}
	if err := PostRevocation(context.Background(), nil, server.URL, record); err != nil {
		t.Fatal(err)
	}
	if posted.Signature != record.Signature {
		t.Fatalf("posted record changed: %+v", posted)
	}
}

func TestFetchRevocationsRejectsRelayForgery(t *testing.T) {
	record := testRevocation(t, "machine-1")
	forged := record
	forged.MachineID = "machine-2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]identity.Revocation{forged})
	}))
	defer server.Close()
	if _, err := FetchRevocations(context.Background(), nil, server.URL, record.OwnerID); err == nil {
		t.Fatal("forged relay record unexpectedly accepted")
	}
}

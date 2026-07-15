package signal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

func signedRevocation(t *testing.T, root []byte, machine string) identity.Revocation {
	t.Helper()
	signer, err := identity.DeriveSigner(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := signer.SignRevocation(machine, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return *record
}

func postRevocation(t *testing.T, base string, record identity.Revocation) *http.Response {
	t.Helper()
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+"/revocations", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSignedRevocationDisconnectsAndBlocksMachine(t *testing.T) {
	root := bytes.Repeat([]byte{0x73}, 32)
	const machine = "machine-1"
	owner, authorization := registrationCredentials(t, root, machine, goodRegistrationSecret)
	s := New()
	server := httptest.NewServer(s.Handler())
	defer server.Close()

	agent := registerAuthorizedAgentWithRegistry(t, server.URL, owner, machine, goodRegistrationSecret, authorization, "opaque")
	defer agent.CloseNow()

	record := signedRevocation(t, root, machine)
	resp := postRevocation(t, server.URL, record)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	// The live slot is removed immediately and can no longer publish discovery.
	s.mu.Lock()
	_, live := s.agents[key(owner, machine)]
	s.mu.Unlock()
	if live {
		t.Fatal("revoked agent remained live")
	}
	entries := getRegistry(t, server.URL, owner, http.StatusOK)
	if len(entries) != 0 {
		t.Fatalf("registry still exposes revoked machine: %+v", entries)
	}

	conn, response, err := dialAuthorizedAgentStatus(server.URL, owner, machine, goodRegistrationSecret, authorization)
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusGone {
		t.Fatalf("revoked registration: status=%v err=%v, want 410", response, err)
	}
	if response.Body != nil {
		response.Body.Close()
	}

	get, err := http.Get(server.URL + "/revocations?owner_id=" + owner)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var records []identity.Revocation
	if err := json.NewDecoder(get.Body).Decode(&records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].MachineID != machine {
		t.Fatalf("GET records = %+v", records)
	}
	if err := identity.VerifyRevocation(&records[0]); err != nil {
		t.Fatalf("GET returned invalid record: %v", err)
	}
}

func TestRevocationRejectsTampering(t *testing.T) {
	record := signedRevocation(t, bytes.Repeat([]byte{0x74}, 32), "machine-1")
	record.MachineID = "machine-2"
	server := httptest.NewServer(New().Handler())
	defer server.Close()
	resp := postRevocation(t, server.URL, record)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRevocationsPersistAcrossServerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocations.json")
	record := signedRevocation(t, bytes.Repeat([]byte{0x75}, 32), "machine-1")

	first := New()
	if err := first.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	firstHTTP := httptest.NewServer(first.Handler())
	resp := postRevocation(t, firstHTTP.URL, record)
	resp.Body.Close()
	firstHTTP.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	second := New()
	if err := second.LoadRevocations(path); err != nil {
		t.Fatal(err)
	}
	second.mu.Lock()
	got, ok := second.revoked[key(record.OwnerID, record.MachineID)]
	second.mu.Unlock()
	if !ok || got.Signature != record.Signature {
		t.Fatalf("persisted record missing or changed: %+v", got)
	}
}

func TestLoadRevocationsFailsClosedOnCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocations.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"revocations":[{"v":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().LoadRevocations(path); err == nil {
		t.Fatal("corrupt revocation file unexpectedly accepted")
	}
}

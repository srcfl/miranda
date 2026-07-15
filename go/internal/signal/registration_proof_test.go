package signal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/identity"
)

const (
	goodRegistrationSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	badRegistrationSecret  = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func registrationCredentials(t *testing.T, root []byte, machine, secret string) (owner, authorization string) {
	t.Helper()
	signer, err := identity.DeriveSigner(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	commitment := sha256.Sum256(raw)
	sig := signer.SignAuth(identity.RegistrationChallenge(machine, hex.EncodeToString(commitment[:])))
	return signer.Address, base64.StdEncoding.EncodeToString(sig)
}

func dialAuthorizedAgentStatus(baseURL, owner, machine, secret, authorization string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL(baseURL, "/agent/signal", map[string]string{"owner_id": owner, "machine_id": machine}), &websocket.DialOptions{HTTPHeader: http.Header{
		AgentRegistrationSecretHeader: []string{secret},
		AgentRegistrationAuthHeader:   []string{authorization},
	}})
}

func registerAuthorizedAgentWithRegistry(t *testing.T, base, owner, machine, secret, authorization, blob string) *websocket.Conn {
	t.Helper()
	c, resp, err := dialAuthorizedAgentStatus(base, owner, machine, secret, authorization)
	if err != nil {
		t.Fatalf("dial authorized agent: status=%v err=%v", resp, err)
	}
	if ready := readMsg(t, c); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}
	writeMsg(t, c, SignalMsg{Type: TypeRegistry, Registry: blob})
	return c
}

func dialAgentWithSecret(t *testing.T, baseURL, owner, machine, secret string) *websocket.Conn {
	t.Helper()
	c, resp, err := dialAgentWithSecretStatus(baseURL, owner, machine, secret)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial agent: status=%d err=%v", resp.StatusCode, err)
		}
		t.Fatalf("dial agent: %v", err)
	}
	return c
}

func dialAgentWithSecretStatus(baseURL, owner, machine, secret string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{}
	if secret != "" {
		opts.HTTPHeader = http.Header{AgentRegistrationSecretHeader: []string{secret}}
	}
	return websocket.Dial(ctx, wsURL(baseURL, "/agent/signal", map[string]string{"owner_id": owner, "machine_id": machine}), opts)
}

func TestAgentRegistrationProofPolicy(t *testing.T) {
	s := New()
	k := key("o", "m")

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.agentProofOKLocked(k, "") {
		t.Fatal("empty proof should be accepted before a secret is learned")
	}
	s.proofs.learn(k, goodRegistrationSecret)
	if !s.agentProofOKLocked(k, goodRegistrationSecret) {
		t.Fatal("matching registration proof should be accepted")
	}
	if s.agentProofOKLocked(k, badRegistrationSecret) {
		t.Fatal("mismatched registration proof should be rejected")
	}
	if s.agentProofOKLocked(k, "") {
		t.Fatal("missing registration proof should be rejected after a secret is learned")
	}
}

func TestAgentRegistrationProofRejectsSpoofedReplacement(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	agent := dialAgentWithSecret(t, srv.URL, "o", "m", goodRegistrationSecret)
	defer agent.CloseNow()
	if ready := readMsg(t, agent); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}

	spoof, resp, err := dialAgentWithSecretStatus(srv.URL, "o", "m", badRegistrationSecret)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if spoof != nil {
		_ = spoof.CloseNow()
	}
	if err == nil {
		t.Fatal("spoofed replacement unexpectedly succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		if resp == nil {
			t.Fatalf("spoofed replacement status = nil, want %d (err=%v)", http.StatusUnauthorized, err)
		}
		t.Fatalf("spoofed replacement status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	// The rejected replacement must not tear down or replace the live agent.
	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer browser.CloseNow()
	if attach := readMsg(t, agent); attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("live agent did not receive attach after rejected spoof: %+v", attach)
	}
}

func TestAgentRegistrationProofAllowsLegitimateReplacement(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	first := dialAgentWithSecret(t, srv.URL, "o", "m", goodRegistrationSecret)
	defer first.CloseNow()
	if ready := readMsg(t, first); ready.Type != TypeReady {
		t.Fatalf("first expected ready, got %q", ready.Type)
	}

	second := dialAgentWithSecret(t, srv.URL, "o", "m", goodRegistrationSecret)
	defer second.CloseNow()
	if ready := readMsg(t, second); ready.Type != TypeReady {
		t.Fatalf("second expected ready, got %q", ready.Type)
	}

	browser := dialJSON(t, wsURL(srv.URL, "/attach", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer browser.CloseNow()
	if attach := readMsg(t, second); attach.Type != TypeAttach || attach.Session == "" {
		t.Fatalf("replacement agent did not receive attach: %+v", attach)
	}
}

func TestAgentRegistrationWithoutProofKeepsLegacyReplacement(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	first := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer first.CloseNow()
	if ready := readMsg(t, first); ready.Type != TypeReady {
		t.Fatalf("first expected ready, got %q", ready.Type)
	}

	second := dialJSON(t, wsURL(srv.URL, "/agent/signal", map[string]string{"owner_id": "o", "machine_id": "m"}))
	defer second.CloseNow()
	if ready := readMsg(t, second); ready.Type != TypeReady {
		t.Fatalf("second expected ready, got %q", ready.Type)
	}
}

func TestOwnerAuthorizedFirstRegistration(t *testing.T) {
	const machine = "machine-1"
	owner, auth := registrationCredentials(t, bytes.Repeat([]byte{0x51}, 32), machine, goodRegistrationSecret)

	if !agentRegistrationAuthorized(owner, machine, goodRegistrationSecret, auth) {
		t.Fatal("valid owner-authorized registration was rejected")
	}
	if agentRegistrationAuthorized(owner, machine, badRegistrationSecret, auth) {
		t.Fatal("authorization must be bound to the exact registration secret")
	}
	if agentRegistrationAuthorized(owner, "other-machine", goodRegistrationSecret, auth) {
		t.Fatal("authorization must be bound to the machine id")
	}
	if agentRegistrationAuthorized(owner, machine, goodRegistrationSecret, "") {
		t.Fatal("a real owner id must fail closed without authorization")
	}
}

func TestOwnerAuthorizedRegistrationBlocksFirstUseSquatting(t *testing.T) {
	const machine = "machine-1"
	owner, auth := registrationCredentials(t, bytes.Repeat([]byte{0x52}, 32), machine, goodRegistrationSecret)
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	spoof, resp, err := dialAuthorizedAgentStatus(srv.URL, owner, machine, badRegistrationSecret, auth)
	if spoof != nil {
		spoof.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first-use spoof status=%v err=%v, want 401", resp, err)
	}

	legitimate, resp, err := dialAuthorizedAgentStatus(srv.URL, owner, machine, goodRegistrationSecret, auth)
	if err != nil {
		t.Fatalf("legitimate first registration: status=%v err=%v", resp, err)
	}
	defer legitimate.CloseNow()
	if ready := readMsg(t, legitimate); ready.Type != TypeReady {
		t.Fatalf("expected ready, got %q", ready.Type)
	}
}

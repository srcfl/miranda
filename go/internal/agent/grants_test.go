// go/internal/agent/grants_test.go — the add-grant CONTROL handler: only the
// session owner's own, verifying, unexpired, this-machine grants persist.
package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

func grantSigner(t *testing.T, fill byte) *identity.Signer {
	t.Helper()
	s, err := identity.DeriveSigner(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func grantPayload(t *testing.T, record string) []byte {
	t.Helper()
	p, err := json.Marshal(map[string]string{"a": "add-grant", "grant": record})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGrantHandlerPersistsAndAcks(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	owner, guest := grantSigner(t, 0x11), grantSigner(t, 0x22)
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	h := rt.grantHandler(owner.Address)

	sg, err := identity.MintGrant(owner, cfg.MachineID, guest.Address, "", "", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := sg.JSON()
	handled, ack := h(grantPayload(t, record))
	if !handled || ack == nil || ack["ack"] != "add-grant:"+sg.GID {
		t.Fatalf("handler = (%v, %v), want ack add-grant:%s", handled, ack, sg.GID)
	}
	raw, err := os.ReadFile(grantPath(dir, sg.GID))
	if err != nil {
		t.Fatalf("grant not persisted: %v", err)
	}
	stored, err := identity.ParseSignedGrant(raw)
	if err != nil || identity.VerifyGrant(stored) != nil || stored.GID != sg.GID {
		t.Fatalf("stored grant does not verify: %v", err)
	}
}

func TestGrantHandlerRejectsWithoutAck(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	owner, other, guest := grantSigner(t, 0x11), grantSigner(t, 0x33), grantSigner(t, 0x22)
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	h := rt.grantHandler(owner.Address)
	now := time.Now()

	foreign, _ := identity.MintGrant(other, cfg.MachineID, guest.Address, "", "", time.Hour, now)
	foreignRec, _ := foreign.JSON()

	wrongMachine, _ := identity.MintGrant(owner, "someone-elses-machine", guest.Address, "", "", time.Hour, now)
	wrongRec, _ := wrongMachine.JSON()

	// Signed for this machine but already dead on arrival.
	expired, err := owner.SignGrant(identity.Grant{
		V: 1, Owner: owner.Address, Machine: cfg.MachineID, Guest: guest.Address,
		Scope: "main", Mode: "ro", NB: now.Add(-2 * time.Hour).Unix(), NA: now.Add(-time.Hour).Unix(),
		GID: "aaaabbbbccccdddd",
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredRec, _ := expired.JSON()

	tampered, _ := identity.MintGrant(owner, cfg.MachineID, guest.Address, "", "", time.Hour, now)
	tamperedRec, _ := tampered.JSON()
	tamperedRec = tamperedRec[:len(tamperedRec)-3] + `x"}` // corrupt the sig tail

	cases := map[string]string{
		"foreign owner": foreignRec,
		"wrong machine": wrongRec,
		"expired":       expiredRec,
		"tampered":      tamperedRec,
		"garbage":       "not json",
		"oversized":     string(bytes.Repeat([]byte("a"), maxGrantRecordBytes+1)),
	}
	for name, rec := range cases {
		handled, ack := h(grantPayload(t, rec))
		if !handled || ack != nil {
			t.Errorf("%s: handler = (%v, %v), want (true, nil)", name, handled, ack)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "grants")); err == nil && len(entries) > 0 {
		t.Fatalf("rejected grants persisted: %v", entries)
	}

	// Not ours at all: tmux window control must fall through.
	tmuxCtl, _ := json.Marshal(map[string]string{"a": "select-window", "t": "@1"})
	if handled, _ := h(tmuxCtl); handled {
		t.Fatal("tmux control was swallowed by the grant handler")
	}
}

func TestChainControlFirstClaimWins(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "box", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	owner, guest := grantSigner(t, 0x11), grantSigner(t, 0x22)
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	chained := chainControl(rt.renameHandler(owner.Address), rt.grantHandler(owner.Address))

	sg, _ := identity.MintGrant(owner, cfg.MachineID, guest.Address, "", "", time.Hour, time.Now())
	record, _ := sg.JSON()
	if handled, ack := chained(grantPayload(t, record)); !handled || ack["ack"] != "add-grant:"+sg.GID {
		t.Fatalf("chained add-grant = (%v, %v)", handled, ack)
	}
	tmuxCtl, _ := json.Marshal(map[string]string{"a": "new-window"})
	if handled, _ := chained(tmuxCtl); handled {
		t.Fatal("tmux control did not fall through the chain")
	}
}

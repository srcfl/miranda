// go/internal/agent/rename_test.go — N1 machine rename: validation, persist,
// live republish, and the session-level HELLO acknowledgement.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

func TestValidMachineName(t *testing.T) {
	valid := []string{"box", "kontoret Mac mini", "räksmörgås", "a", strings.Repeat("x", 64)}
	for _, name := range valid {
		if !ValidMachineName(name) {
			t.Errorf("ValidMachineName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", " padded ", "trailing ", "tab\tinside", "line\nbreak", "esc\x1b[31m", strings.Repeat("x", 65)}
	for _, name := range invalid {
		if ValidMachineName(name) {
			t.Errorf("ValidMachineName(%q) = true, want false", name)
		}
	}
}

func TestRenameMachinePersistsNameAndOwnerBlob(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrInit(dir, "old-name", "https://relay.example"); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionOwner(dir, "owner-a", "blob-a", ""); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionOwner(dir, "owner-b", "blob-b", ""); err != nil {
		t.Fatal(err)
	}

	if err := RenameMachine(dir, "owner-a", "new-name", "blob-a2"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MachineName != "new-name" {
		t.Fatalf("persisted machine_name = %q, want %q", cfg.MachineName, "new-name")
	}
	if got := RegistryForOwner(dir, "owner-a"); got != "blob-a2" {
		t.Fatalf("owner-a blob = %q, want %q", got, "blob-a2")
	}
	if got := RegistryForOwner(dir, "owner-b"); got != "blob-b" {
		t.Fatalf("owner-b blob = %q — rename must only touch the requesting owner's slot", got)
	}
}

// fakeConn captures signaling writes for republish assertions.
type fakeConn struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (f *fakeConn) Write(_ context.Context, _ websocket.MessageType, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, append([]byte(nil), p...))
	return nil
}

func (f *fakeConn) all() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.msgs...)
}

func TestRenameHandlerAppliesPersistsAndRepublishes(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "old-name", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	const owner = "owner-a"
	if err := ProvisionOwner(dir, owner, "blob-old", ""); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	fc := &fakeConn{}
	unregister := rt.registerSignaling(owner, &signalWriter{c: fc}, context.Background())
	defer unregister()

	newBlob := base64.StdEncoding.EncodeToString([]byte("resealed-record"))
	payload, _ := json.Marshal(map[string]string{"a": "rename-machine", "n": "new-name", "blob": newBlob})
	handled, hello := rt.renameHandler(owner)(payload)
	if !handled || hello != "new-name" {
		t.Fatalf("handler = (%v, %q), want (true, \"new-name\")", handled, hello)
	}
	if got := RegistryForOwner(dir, owner); got != newBlob {
		t.Fatalf("persisted blob = %q, want the re-sealed one", got)
	}
	if got := rt.machineName(); got != "new-name" {
		t.Fatalf("live machine name = %q, want %q", got, "new-name")
	}
	var republished bool
	for _, raw := range fc.all() {
		var m signal.SignalMsg
		if json.Unmarshal(raw, &m) == nil && m.Type == signal.TypeRegistry && m.Registry == newBlob {
			republished = true
		}
	}
	if !republished {
		t.Fatal("renamed record was not republished on the live signaling connection")
	}
}

func TestRenameHandlerRejectsBadInputWithoutHello(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrInit(dir, "old-name", "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(cfg, []string{"sh"}, nil)
	h := rt.renameHandler("owner-a")

	// Not ours: plain tmux window control must fall through.
	tmuxCtl, _ := json.Marshal(map[string]string{"a": "select-window", "t": "@1"})
	if handled, _ := h(tmuxCtl); handled {
		t.Fatal("tmux control was swallowed by the rename handler")
	}
	// Ours but invalid: swallowed, no HELLO, nothing persisted.
	bad := []map[string]string{
		{"a": "rename-machine", "n": "", "blob": base64.StdEncoding.EncodeToString([]byte("x"))},
		{"a": "rename-machine", "n": "ok-name", "blob": ""},
		{"a": "rename-machine", "n": "ok-name", "blob": "not!!base64"},
		{"a": "rename-machine", "n": "bad\nname", "blob": base64.StdEncoding.EncodeToString([]byte("x"))},
		{"a": "rename-machine", "n": "big", "blob": base64.StdEncoding.EncodeToString(make([]byte, maxRenameBlobBytes+1))},
	}
	for _, c := range bad {
		payload, _ := json.Marshal(c)
		handled, hello := h(payload)
		if !handled || hello != "" {
			t.Fatalf("bad rename %v = (%v, %q), want (true, \"\")", c, handled, hello)
		}
	}
	if got := RegistryForOwner(dir, "owner-a"); got != "" {
		t.Fatalf("bad rename persisted a blob: %q", got)
	}
	if got := rt.machineName(); got != "old-name" {
		t.Fatalf("bad rename changed the live name to %q", got)
	}
}

// TestSessionRenameControlRepliesHello pins the acknowledgement path: a CONTROL
// frame claimed by the handler with a new name makes the session re-HELLO with
// that name — what the renaming client waits for.
func TestSessionRenameControlRepliesHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentPriv, agentPub, _ := noise.GenerateStatic()
	clientPriv, clientPub, _ := noise.GenerateStatic()
	clientMC, agentMC := peer.Pipe()

	control := func(payload []byte) (bool, string) {
		var c struct {
			A string `json:"a"`
			N string `json:"n"`
		}
		if json.Unmarshal(payload, &c) != nil || c.A != "rename-machine" {
			return false, ""
		}
		return true, c.N
	}

	done := make(chan error, 1)
	go func() {
		s, err := peer.RunResponder(ctx, agentMC, agentPriv, clientPub)
		if err != nil {
			done <- err
			return
		}
		done <- RunAgentSession(ctx, agentMC, s, blockingShell{stop: ctx.Done()}, "old-name", nil, 0, control)
	}()
	cs, err := peer.RunInitiator(ctx, clientMC, clientPriv, agentPub)
	if err != nil {
		t.Fatal(err)
	}

	// Initial HELLO carries the old name.
	frame := recvFrame(t, ctx, clientMC, cs)
	if typ, payload, _ := noise.DecodeFrame(frame); typ != noise.FrameHello || !strings.Contains(string(payload), "old-name") {
		t.Fatalf("expected initial HELLO with old name, got type %d payload %s", typ, payload)
	}

	payload, _ := json.Marshal(map[string]string{"a": "rename-machine", "n": "renamed-box"})
	ct, err := cs.Encrypt(noise.EncodeControl(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := clientMC.Send(ct); err != nil {
		t.Fatal(err)
	}

	for {
		frame := recvFrame(t, ctx, clientMC, cs)
		typ, payload, err := noise.DecodeFrame(frame)
		if err != nil || typ != noise.FrameHello {
			continue
		}
		var meta map[string]string
		if json.Unmarshal(payload, &meta) == nil && meta["name"] == "renamed-box" {
			break // acknowledged
		}
	}
	cancel()
	<-done
}

// blockingShell is a Shell that produces no output and stays alive until the
// test ends — the session lives on while control frames round-trip.
type blockingShell struct{ stop <-chan struct{} }

func (b blockingShell) Read([]byte) (int, error)    { <-b.stop; return 0, context.Canceled }
func (b blockingShell) Write(p []byte) (int, error) { return len(p), nil }
func (b blockingShell) Resize(uint16, uint16) error { return nil }
func (b blockingShell) Close() error                { return nil }

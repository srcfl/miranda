// go/internal/agent/rename.go
//
// Machine rename (N1). The agent cannot seal registry records (it never holds
// the owner root — targets are expendable), so a rename arrives from an owner
// client over the authenticated session as an agent-level CONTROL command:
//
//	{a:"rename-machine", n:<new display name>, blob:<base64 re-sealed record>}
//
// The agent validates, persists the name + the owner's opaque record, pushes
// the record to the relay on the live signaling connection (the relay replaces
// the blob it holds for this registration), and re-HELLOs the session — the
// client's acknowledgement. Other devices converge via the registry's ts.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

// maxRenameBlobBytes mirrors the relay's registry blob cap: a blob the relay
// would refuse to hold is refused here too, before it is persisted.
const maxRenameBlobBytes = 16 << 10

type renameControl struct {
	A    string `json:"a"`
	N    string `json:"n"`
	Blob string `json:"blob"`
}

// ValidMachineName bounds a client-proposed display name: 1..64 runes, no
// control characters, no surrounding whitespace. The name travels E2E only
// (HELLO + sealed registry records) — the relay never sees it — so this guards
// terminals and logs, not the relay.
func ValidMachineName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

// RenameMachine persists a renamed display name plus the requesting owner's
// re-sealed registry record. The record stays opaque to the agent (same trust
// shape as ProvisionOwner); only the named owner's slot is touched.
func RenameMachine(dir, ownerID, name, blob string) error {
	cfg := &Config{}
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return err
	}
	cfg.MachineName = name
	if cfg.OwnerRegistry == nil {
		cfg.OwnerRegistry = make(map[string]string)
	}
	cfg.OwnerRegistry[ownerID] = blob
	return save(dir, cfg)
}

// renameState carries the runtime's live-rename bookkeeping: the current
// display name (renamed mid-run by an owner session) and the live signaling
// writers used to republish a renamed record without a reconnect.
type renameState struct {
	mu      sync.Mutex
	name    string                     // current display name; "" = use cfg.MachineName
	writers map[string]*ownerSignaling // live per-owner signaling connections
}

// ownerSignaling is one live serveOnce connection: the serialized writer plus
// the connection's context (writes after the connection dies fail fast on it).
type ownerSignaling struct {
	w   *signalWriter
	ctx context.Context
}

// machineName returns the live display name (a mid-run rename wins over the
// boot-time config value).
func (rt *Runtime) machineName() string {
	rt.rename.mu.Lock()
	defer rt.rename.mu.Unlock()
	if rt.rename.name != "" {
		return rt.rename.name
	}
	return rt.cfg.MachineName
}

func (rt *Runtime) setMachineName(name string) {
	rt.rename.mu.Lock()
	rt.rename.name = name
	rt.rename.mu.Unlock()
}

// registerSignaling makes a live serveOnce connection reachable for mid-run
// registry republish; the returned func unregisters it (connection gone).
func (rt *Runtime) registerSignaling(owner string, w *signalWriter, ctx context.Context) func() {
	rt.rename.mu.Lock()
	if rt.rename.writers == nil {
		rt.rename.writers = make(map[string]*ownerSignaling)
	}
	conn := &ownerSignaling{w: w, ctx: ctx}
	rt.rename.writers[owner] = conn
	rt.rename.mu.Unlock()
	return func() {
		rt.rename.mu.Lock()
		if rt.rename.writers[owner] == conn { // only remove our own registration
			delete(rt.rename.writers, owner)
		}
		rt.rename.mu.Unlock()
	}
}

// republishRegistry pushes the owner's (re-sealed) record on the live signaling
// connection, replacing the blob the relay holds for this registration.
// Best-effort: with no live connection (LAN-only, relay down) the renamed record
// still publishes on the next reconnect — serveOnce reads it from disk.
func (rt *Runtime) republishRegistry(owner string) {
	rt.rename.mu.Lock()
	conn := rt.rename.writers[owner]
	rt.rename.mu.Unlock()
	if conn == nil {
		return
	}
	blob := RegistryForOwner(rt.cfg.Dir, owner)
	if blob == "" {
		return
	}
	if msg, err := json.Marshal(signal.SignalMsg{Type: signal.TypeRegistry, Registry: blob}); err == nil {
		_ = conn.w.write(conn.ctx, msg)
	}
}

// renameHandler builds the per-session ControlHandler for one authenticated
// owner. Only "rename-machine" is claimed; anything else falls through to tmux
// window control. An invalid rename is swallowed (handled, no HELLO): the
// channel is owner-authenticated, so bad input is a client bug, not an attack
// to punish the session for.
func (rt *Runtime) renameHandler(owner string) ControlHandler {
	return func(payload []byte) (bool, map[string]string) {
		var c renameControl
		if json.Unmarshal(payload, &c) != nil || c.A != "rename-machine" {
			return false, nil
		}
		if !ValidMachineName(c.N) || c.Blob == "" {
			return true, nil
		}
		if raw, err := base64.StdEncoding.DecodeString(c.Blob); err != nil || len(raw) == 0 || len(raw) > maxRenameBlobBytes {
			return true, nil
		}
		if err := RenameMachine(rt.cfg.Dir, owner, c.N, c.Blob); err != nil {
			if rt.Logf != nil {
				rt.Logf("event=rename_failed err=%v", err)
			}
			return true, nil
		}
		rt.setMachineName(c.N)
		rt.republishRegistry(owner)
		if rt.Logf != nil {
			rt.Logf("event=renamed name=%q", c.N)
		}
		return true, map[string]string{"name": c.N}
	}
}

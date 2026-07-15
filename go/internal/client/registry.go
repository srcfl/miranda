// go/internal/client/registry.go
package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// registryEntry is the relay's blind wire shape: GET /registry?owner_id=W ->
// [{machine_id, blob}]. blob is base64(nonce||ciphertext||tag), sealed by the
// owner client (AAD = machine_id). The relay never opens it.
type registryEntry struct {
	MachineID string `json:"machine_id"`
	Blob      string `json:"blob"`
}

// registryRecord is the sealed plaintext an agent publishes: {v, name, host_pub,
// signal_url, ts}. Only an owner-root holder can open the blob to recover it.
type registryRecord struct {
	V         int    `json:"v"`
	Name      string `json:"name"`
	HostPub   string `json:"host_pub"`
	SignalURL string `json:"signal_url"`
	TS        int64  `json:"ts"`
}

// SealRegistryMachine creates the opaque discovery record provisioned to an
// agent during pairing. Encryption happens on the owner client; the agent only
// stores and republishes the blob and never receives the registry key or root.
func SealRegistryMachine(id *Identity, m Machine) (string, error) {
	if !id.HasRootedIdentity() {
		return "", fmt.Errorf("registry: identity has no secret root")
	}
	rec := registryRecord{V: 1, Name: m.Name, HostPub: m.HostPubHex, SignalURL: m.SignalURL, TS: time.Now().Unix()}
	pt, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	secret := id.Secret()
	defer zeroBytes(secret)
	key, err := identity.RegistryKey(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	blob, err := identity.SealRecord(key, nonce, pt, m.MachineID)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

// FetchRegistry asks the relay for this owner's live device records and decrypts
// them. Forged/garbage blobs (sealed without the owner root) fail to open and are
// silently dropped. Best-effort: a relay error or a legacy identity returns
// nil so callers can fall back to the local machines.json without surfacing noise.
func FetchRegistry(ctx context.Context, hc *http.Client, signalURL string, id *Identity) ([]Machine, error) {
	if !id.HasRootedIdentity() {
		return nil, nil
	}
	secret := id.Secret()
	defer zeroBytes(secret)
	key, err := identity.RegistryKey(secret)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	url := strings.TrimRight(signalURL, "/") + "/registry?owner_id=" + neturl.QueryEscape(id.OwnerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry: relay returned %s", resp.Status)
	}
	var entries []registryEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}

	var machines []Machine
	for _, e := range entries {
		blob, err := base64.StdEncoding.DecodeString(e.Blob)
		if err != nil {
			continue // not even valid base64 — drop
		}
		pt, err := identity.OpenRecord(key, blob, e.MachineID)
		if err != nil {
			continue // forged/garbage/wrong-machine — drop the forgery
		}
		var rec registryRecord
		if err := json.Unmarshal(pt, &rec); err != nil {
			continue // opened but malformed — drop
		}
		su := rec.SignalURL
		if su == "" {
			su = signalURL // always attachable: fall back to the relay we fetched from
		}
		machines = append(machines, Machine{
			Name:       rec.Name,
			MachineID:  e.MachineID,
			HostPubHex: rec.HostPub,
			SignalURL:  su,
		})
	}
	return machines, nil
}

// MergeMachines unions local and discovered machines by MachineID. A machine
// present locally keeps its local entry (local wins) — the user's pinned
// machines.json is authoritative; discovered-only machines are appended. Order is
// local-first, then the discovered newcomers, so existing list output is stable.
func MergeMachines(local, discovered []Machine) []Machine {
	seen := make(map[string]bool, len(local))
	out := make([]Machine, 0, len(local)+len(discovered))
	for _, m := range local {
		seen[m.MachineID] = true
		out = append(out, m)
	}
	for _, m := range discovered {
		if seen[m.MachineID] {
			continue // local wins
		}
		seen[m.MachineID] = true
		out = append(out, m)
	}
	return out
}

// ResolveMachine finds a machine by name, preferring the local store, then the
// discovered registry. It returns a copy and whether it came from discovery.
func ResolveMachine(local, discovered []Machine, name string) (Machine, bool, bool) {
	for _, m := range local {
		if m.Name == name {
			return m, true, false
		}
	}
	for _, m := range discovered {
		if m.Name == name {
			return m, true, true
		}
	}
	return Machine{}, false, false
}

func seenPath(dir string) string { return filepath.Join(dir, "seen.json") }

type seenSet struct {
	MachineIDs []string `json:"machine_ids"`
}

// NotifyNewDevices prints a one-line "new device joined" notice (to w) the first
// time a machine_id is seen, then persists the union to <dir>/seen.json so the
// notice fires exactly once per device. It is pure-ish — the writer and dir are
// injected — so the wiring stays trivially testable.
func NotifyNewDevices(w io.Writer, dir string, machines []Machine) error {
	seen := loadSeen(dir)
	known := make(map[string]bool, len(seen.MachineIDs))
	for _, id := range seen.MachineIDs {
		known[id] = true
	}
	changed := false
	for _, m := range machines {
		if m.MachineID == "" || known[m.MachineID] {
			continue
		}
		known[m.MachineID] = true
		seen.MachineIDs = append(seen.MachineIDs, m.MachineID)
		changed = true
		fmt.Fprintf(w, "📣 new machine %q joined your Miranda identity\n", m.Name)
	}
	if !changed {
		return nil
	}
	return saveSeen(dir, seen)
}

// loadSeen reads the seen-set; a missing or unreadable file is an empty set (so a
// first run notifies for everything and a corrupt file degrades to re-notifying,
// never to a hard error).
func loadSeen(dir string) seenSet {
	var s seenSet
	data, err := os.ReadFile(seenPath(dir))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

func saveSeen(dir string, s seenSet) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(seenPath(dir), data, 0o600)
}

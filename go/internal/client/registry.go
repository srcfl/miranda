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
// agent during pairing (and re-sealed on rename). Encryption happens on the
// owner client; the agent only stores and republishes the blob and never
// receives the registry key or root. The second result is the record's ts —
// the name's last-writer-wins timestamp (see MergeMachines).
func SealRegistryMachine(id *Identity, m Machine) (string, int64, error) {
	if !id.HasRootedIdentity() {
		return "", 0, fmt.Errorf("registry: identity has no secret root")
	}
	ts := time.Now().Unix()
	rec := registryRecord{V: 1, Name: m.Name, HostPub: m.HostPubHex, SignalURL: m.SignalURL, TS: ts}
	pt, err := json.Marshal(rec)
	if err != nil {
		return "", 0, err
	}
	secret := id.Secret()
	defer zeroBytes(secret)
	key, err := identity.RegistryKey(secret)
	if err != nil {
		return "", 0, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", 0, err
	}
	blob, err := identity.SealRecord(key, nonce, pt, m.MachineID)
	if err != nil {
		return "", 0, err
	}
	return base64.StdEncoding.EncodeToString(blob), ts, nil
}

// FetchRegistry asks the relay for this owner's live device records and decrypts
// them. Forged/garbage blobs (sealed without the owner root) fail to open and are
// silently dropped. Best-effort: a relay error or a legacy identity returns
// nil so callers can fall back to the local machines.json without surfacing noise.
func FetchRegistry(ctx context.Context, hc *http.Client, signalURL string, id *Identity) ([]Machine, error) {
	if !id.HasRootedIdentity() {
		return nil, nil
	}
	entries, err := fetchRegistryEntries(ctx, hc, signalURL, id.OwnerID)
	if err != nil {
		return nil, err
	}
	return decodeRegistryEntries(entries, id, signalURL)
}

// fetchRegistryEntries is the network half: it returns the relay's blind wire
// shape, which is also exactly what the pre-attach cache stores.
func fetchRegistryEntries(ctx context.Context, hc *http.Client, signalURL, ownerID string) ([]registryEntry, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	url := strings.TrimRight(signalURL, "/") + "/registry?owner_id=" + neturl.QueryEscape(ownerID)
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
	return entries, nil
}

// decodeRegistryEntries is the crypto half: it opens each sealed blob with the
// owner root. Forged/garbage blobs fail to open and are silently dropped, live
// or cached alike.
func decodeRegistryEntries(entries []registryEntry, id *Identity, signalURL string) ([]Machine, error) {
	if !id.HasRootedIdentity() {
		return nil, nil
	}
	secret := id.Secret()
	defer zeroBytes(secret)
	key, err := identity.RegistryKey(secret)
	if err != nil {
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
			NameTS:     rec.TS,
		})
	}
	return machines, nil
}

// MergeMachines unions local and discovered machines by MachineID. A verified
// registry record is canonical for HostPubHex and SignalURL (unsigned
// machines.json must not win a pin). The display name is last-writer-wins on
// NameTS: a registry record sealed after the local name was set carries a
// rename made on another device, so it wins; a local rename not yet delivered
// to the machine (newer NameTS) keeps winning until the machine republishes.
// Discovered-only machines are appended. Order is local-first, then newcomers.
func MergeMachines(local, discovered []Machine) []Machine {
	byID := make(map[string]Machine, len(local)+len(discovered))
	order := make([]string, 0, len(local)+len(discovered))
	for _, m := range local {
		if m.MachineID == "" {
			continue
		}
		if _, ok := byID[m.MachineID]; !ok {
			order = append(order, m.MachineID)
		}
		byID[m.MachineID] = m
	}
	for _, m := range discovered {
		if m.MachineID == "" {
			continue
		}
		prev, ok := byID[m.MachineID]
		if !ok {
			order = append(order, m.MachineID)
			byID[m.MachineID] = m
			continue
		}
		if m.HostPubHex != "" {
			prev.HostPubHex = m.HostPubHex
		}
		if m.SignalURL != "" {
			prev.SignalURL = m.SignalURL
		}
		if m.Name != "" && (prev.Name == "" || m.NameTS >= prev.NameTS) {
			prev.Name = m.Name
			prev.NameTS = m.NameTS
		}
		byID[m.MachineID] = prev
	}
	out := make([]Machine, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// ResolveMachine finds a machine by name on the merged list (registry host_pub
// and signal beat the unsigned local cache). The third result is true when the
// name was not in the local pin set.
func ResolveMachine(local, discovered []Machine, name string) (Machine, bool, bool) {
	fromLocal := false
	for _, m := range local {
		if m.Name == name {
			fromLocal = true
			break
		}
	}
	for _, m := range MergeMachines(local, discovered) {
		if m.Name == name {
			return m, true, !fromLocal
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

// go/internal/agent/grantstore.go
//
// The agent's read side of the guest-grant store (G1c enforcement): find the
// grant backing a guest offer, tombstone a revoked gid, and sweep expired grant
// files at startup. AddGrant (the write side) lives in grants.go.
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

func tombstonePath(dir string) string { return filepath.Join(grantsDir(dir), "revoked.json") }

// loadTombstones returns the set of revoked gids. A missing or unreadable file
// is an empty set — a grant is enforced on its signature and clock regardless,
// and revocation is the owner's tool, so a lost tombstone file fails toward
// "still valid until it expires", bounded by the 24 h TTL cap.
func loadTombstones(dir string) map[string]bool {
	set := map[string]bool{}
	raw, err := os.ReadFile(tombstonePath(dir))
	if err != nil {
		return set
	}
	var gids []string
	if json.Unmarshal(raw, &gids) != nil {
		return set
	}
	for _, g := range gids {
		set[g] = true
	}
	return set
}

// TombstoneGrant records gid as revoked (idempotent) and removes its grant file
// so no future attach can load it.
func TombstoneGrant(dir, gid string) error {
	set := loadTombstones(dir)
	set[gid] = true
	gids := make([]string, 0, len(set))
	for g := range set {
		gids = append(gids, g)
	}
	data, err := json.Marshal(gids)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(grantsDir(dir), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tombstonePath(dir), data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(grantPath(dir, gid))
	return nil
}

// findValidGuestGrant returns the grant that authorizes a guest offer, or nil.
// A grant qualifies only if it names this owner and machine, is bound to the
// offering guest key, verifies against the owner signature, is not tombstoned,
// and its window covers now. Enforcement runs on EVERY attach, so a grant that
// has since expired or been revoked stops working without touching the file.
func findValidGuestGrant(dir, owner, machine, guest string, now time.Time) *identity.SignedGrant {
	entries, err := os.ReadDir(grantsDir(dir))
	if err != nil {
		return nil
	}
	tombstoned := loadTombstones(dir)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name() == "revoked.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(grantsDir(dir), e.Name()))
		if err != nil {
			continue
		}
		sg, err := identity.ParseSignedGrant(raw)
		if err != nil {
			continue
		}
		if sg.Guest != guest || sg.Owner != owner || sg.Machine != machine {
			continue
		}
		if tombstoned[sg.GID] {
			continue
		}
		if identity.VerifyGrant(sg) != nil || sg.ValidAt(now) != nil {
			continue
		}
		return sg
	}
	return nil
}

// sweepExpiredGrants removes grant files whose window has fully closed (past na
// plus the skew tolerance), so the store does not grow without bound. Tombstones
// are kept — they are tiny and must outlive the grant file. Called at startup.
func sweepExpiredGrants(dir string, now time.Time) {
	entries, err := os.ReadDir(grantsDir(dir))
	if err != nil {
		return
	}
	cutoff := now.Add(-identity.GrantSkew).Unix()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name() == "revoked.json" {
			continue
		}
		p := filepath.Join(grantsDir(dir), e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sg, err := identity.ParseSignedGrant(raw)
		if err != nil || sg.NA < cutoff {
			_ = os.Remove(p)
		}
	}
}

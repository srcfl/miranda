// go/internal/client/shares.go
//
// Local share state on the client (G1d). Two sides:
//   - OWNER: every mint is recorded under <dir>/shares/<gid>.json so
//     `mir share ls` can list it and `mir share revoke` can find the machine
//     to deliver the tombstone to. Purely local bookkeeping — the agent's
//     grant store is the authority.
//   - GUEST: grants received with `mir join` live under <dir>/grants/ (G1b).
//     Helpers here read them for `mir ls`/attach display and sweep entries
//     whose window has fully closed.
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// OwnerShare is one recorded mint.
type OwnerShare struct {
	Record      string `json:"record"` // the signed grant, verbatim
	MachineName string `json:"machine_name"`
	Revoked     bool   `json:"revoked"`

	Grant identity.SignedGrant `json:"-"` // parsed from Record on load
}

func sharesDir(dir string) string { return filepath.Join(dir, "shares") }

// SaveOwnerShare records a successful mint.
func SaveOwnerShare(dir string, record, machineName string) error {
	sg, err := identity.ParseSignedGrant([]byte(record))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sharesDir(dir), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(OwnerShare{Record: record, MachineName: machineName})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sharesDir(dir), sg.GID+".json"), data, 0o600)
}

// ListOwnerShares returns recorded mints, newest expiry first.
func ListOwnerShares(dir string) ([]OwnerShare, error) {
	entries, err := os.ReadDir(sharesDir(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OwnerShare
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sharesDir(dir), e.Name()))
		if err != nil {
			continue
		}
		var s OwnerShare
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		sg, err := identity.ParseSignedGrant([]byte(s.Record))
		if err != nil {
			continue
		}
		s.Grant = *sg
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Grant.NA > out[j].Grant.NA })
	return out, nil
}

// MarkShareRevoked flips the local revoked flag after the agent acked the
// tombstone.
func MarkShareRevoked(dir, gid string) error {
	p := filepath.Join(sharesDir(dir), gid+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var s OwnerShare
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	s.Revoked = true
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// ResolveShareGID matches a gid prefix against recorded mints: exactly one
// match wins; none or several is an error the caller shows verbatim.
func ResolveShareGID(dir, prefix string) (OwnerShare, error) {
	shares, err := ListOwnerShares(dir)
	if err != nil {
		return OwnerShare{}, err
	}
	var hits []OwnerShare
	for _, s := range shares {
		if strings.HasPrefix(s.Grant.GID, prefix) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return OwnerShare{}, fmt.Errorf("no share matches %q — see `mir share ls`", prefix)
	default:
		return OwnerShare{}, fmt.Errorf("%d shares match %q — use more of the id from `mir share ls`", len(hits), prefix)
	}
}

// ListGuestGrants returns the grants this identity received as a guest.
func ListGuestGrants(dir string) []identity.SignedGrant {
	entries, err := os.ReadDir(filepath.Join(dir, "grants"))
	if err != nil {
		return nil
	}
	var out []identity.SignedGrant
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name() == "revoked.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "grants", e.Name()))
		if err != nil {
			continue
		}
		sg, err := identity.ParseSignedGrant(raw)
		if err != nil {
			continue
		}
		out = append(out, *sg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NA > out[j].NA })
	return out
}

// GuestGrantFor returns the best (latest-expiring) grant covering machineID,
// or nil.
func GuestGrantFor(dir, machineID string) *identity.SignedGrant {
	for _, g := range ListGuestGrants(dir) {
		if g.Machine == machineID {
			g := g
			return &g
		}
	}
	return nil
}

// SweepGuestState removes guest machine entries (Machine.Owner set) whose
// every grant window has fully closed (past na + skew), plus the closed grant
// files themselves — so `mir ls` does not accumulate dead shares. Live or
// merely offline shares are untouched.
func SweepGuestState(dir string, now time.Time) {
	cutoff := now.Add(-identity.GrantSkew).Unix()
	live := map[string]bool{}
	for _, g := range ListGuestGrants(dir) {
		if g.NA >= cutoff {
			live[g.Machine] = true
			continue
		}
		_ = os.Remove(filepath.Join(dir, "grants", g.GID+".json"))
	}
	machines, err := ListMachines(dir)
	if err != nil {
		return
	}
	for _, m := range machines {
		if m.Owner != "" && !live[m.MachineID] {
			_ = RemoveMachine(dir, m.MachineID)
		}
	}
}

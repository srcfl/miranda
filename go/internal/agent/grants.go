// go/internal/agent/grants.go
//
// Guest-grant storage and the add-grant CONTROL handler (G1b). The agent only
// receives and persists here; attach-time enforcement (the guest branch, the
// ro mirror, expiry deadlines, tombstones) is G1c. A grant is accepted only
// from the authenticated session of the owner who signed it, for this very
// machine, and only while its window has not already closed — so the store
// can never hold a record the signature or clock story wouldn't back.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// maxGrantRecordBytes bounds the CONTROL payload before parsing; a real grant
// record is a few hundred bytes.
const maxGrantRecordBytes = 4096

type grantControl struct {
	A     string `json:"a"`
	Grant string `json:"grant"`
}

func grantsDir(dir string) string { return filepath.Join(dir, "grants") }

// grantPath is the stored record for one gid. The gid is validated by
// VerifyGrant's charset rule before it ever names a file.
func grantPath(dir, gid string) string { return filepath.Join(grantsDir(dir), gid+".json") }

// AddGrant persists a verified grant record under its gid.
func AddGrant(dir string, sg *identity.SignedGrant) error {
	record, err := sg.JSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(grantsDir(dir), 0o700); err != nil {
		return err
	}
	return os.WriteFile(grantPath(dir, sg.GID), []byte(record), 0o600)
}

// grantHandler builds the per-session ControlHandler accepting add-grant from
// one authenticated owner. Invalid input is swallowed (handled, no ack): the
// channel is owner-authenticated, so bad input is a client bug, not an attack
// to punish the session for — the missing ack is what the client reports.
func (rt *Runtime) grantHandler(owner string) ControlHandler {
	return func(payload []byte) (bool, map[string]string) {
		var c grantControl
		if json.Unmarshal(payload, &c) != nil || c.A != "add-grant" {
			return false, nil
		}
		if len(c.Grant) == 0 || len(c.Grant) > maxGrantRecordBytes {
			return true, nil
		}
		sg, err := identity.ParseSignedGrant([]byte(c.Grant))
		if err == nil {
			err = identity.VerifyGrant(sg)
		}
		if err != nil {
			rt.logGrantRejected("unverifiable", err)
			return true, nil
		}
		// The signer must be the owner on THIS session — an owner can install
		// only their own grants — and the grant must name this machine and not
		// be dead on arrival.
		if sg.Owner != owner {
			rt.logGrantRejected("foreign owner", fmt.Errorf("grant owner %.8s… is not the session owner", sg.Owner))
			return true, nil
		}
		if sg.Machine != rt.cfg.MachineID {
			rt.logGrantRejected("wrong machine", fmt.Errorf("grant names %q", sg.Machine))
			return true, nil
		}
		if time.Now().Unix() > sg.NA {
			rt.logGrantRejected("already expired", nil)
			return true, nil
		}
		if err := AddGrant(rt.cfg.Dir, sg); err != nil {
			rt.logGrantRejected("persist failed", err)
			return true, nil
		}
		if rt.Logf != nil {
			rt.Logf("event=grant_added gid=%s guest=%.8s… mode=%s scope=%q na=%d", sg.GID, sg.Guest, sg.Mode, sg.Scope, sg.NA)
		}
		return true, map[string]string{"name": rt.machineName(), "ack": "add-grant:" + sg.GID}
	}
}

func (rt *Runtime) logGrantRejected(reason string, err error) {
	if rt.Logf == nil {
		return
	}
	if err != nil {
		rt.Logf("event=grant_rejected reason=%q err=%v", reason, err)
		return
	}
	rt.Logf("event=grant_rejected reason=%q", reason)
}

// chainControl composes per-session control handlers; the first one that
// claims the payload wins.
func chainControl(handlers ...ControlHandler) ControlHandler {
	return func(payload []byte) (bool, map[string]string) {
		for _, h := range handlers {
			if handled, ack := h(payload); handled {
				return true, ack
			}
		}
		return false, nil
	}
}

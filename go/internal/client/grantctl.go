// go/internal/client/grantctl.go
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// ErrGrantUnconfirmed: add-grant was delivered on the session but the agent
// never acked — most likely an agent from before sharing shipped. Unlike a
// rename, an unconfirmed grant must NOT stand: the caller aborts the share.
var ErrGrantUnconfirmed = errors.New("machine did not confirm the share")

// GrantOverSession delivers a signed grant record to the agent over an
// established owner session and waits for the acknowledging HELLO (the agent
// re-HELLOs with ack "add-grant:<gid>" after verifying and persisting). The
// initial HELLO and any other frames are skipped.
func GrantOverSession(ctx context.Context, mc peer.MsgConn, sess *noise.Session, record, gid string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	payload, err := json.Marshal(map[string]string{"a": "add-grant", "grant": record})
	if err != nil {
		return err
	}
	if err := newSender(mc, sess).send(noise.EncodeControl(payload)); err != nil {
		return fmt.Errorf("share: send failed: %w", err)
	}

	want := "add-grant:" + gid
	for {
		ct, err := mc.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ErrGrantUnconfirmed
			}
			return err
		}
		pt, err := sess.Decrypt(ct)
		if err != nil {
			return err
		}
		typ, payload, err := noise.DecodeFrame(pt)
		if err != nil || typ != noise.FrameHello {
			continue
		}
		var meta map[string]string
		if json.Unmarshal(payload, &meta) == nil && meta["ack"] == want {
			return nil
		}
	}
}

var guestGIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// SaveGuestGrant stores the grant record a guest received with an invite, so
// the client can show and later use its own access (G1c/G1d). Keyed by gid,
// which is charset-checked before it names a file.
func SaveGuestGrant(dir, gid, record string) error {
	if !guestGIDRe.MatchString(gid) {
		return fmt.Errorf("grant: invalid gid")
	}
	p := filepath.Join(dir, "grants")
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p, gid+".json"), []byte(record), 0o600)
}

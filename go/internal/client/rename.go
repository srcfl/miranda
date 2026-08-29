// go/internal/client/rename.go
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// ErrRenameUnconfirmed: the rename command was delivered on the session but no
// fresh HELLO with the new name came back in time — most likely an agent from
// before machine rename shipped. The local rename stands either way.
var ErrRenameUnconfirmed = errors.New("machine did not confirm the rename")

// RenameOverSession delivers a rename to the agent over an established session:
// the new display name plus the owner-resealed registry blob (the agent cannot
// seal records itself). It then waits for the agent's fresh HELLO carrying the
// new name — the acknowledgement that the rename was applied and republished.
// The agent's initial HELLO (old name) and any other frames are skipped.
func RenameOverSession(ctx context.Context, mc peer.MsgConn, sess *noise.Session, newName, blob string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	payload, err := json.Marshal(map[string]string{"a": "rename-machine", "n": newName, "blob": blob})
	if err != nil {
		return err
	}
	if err := newSender(mc, sess).send(noise.EncodeControl(payload)); err != nil {
		return fmt.Errorf("rename: send failed: %w", err)
	}

	for {
		ct, err := mc.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ErrRenameUnconfirmed
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
		if json.Unmarshal(payload, &meta) == nil && meta["name"] == newName {
			return nil
		}
	}
}

package client

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// SessionDial opens one attach (transport + Noise) and returns cleanup for that
// attempt. ReconnectLoop calls it after each drop.
type SessionDial func(ctx context.Context) (peer.MsgConn, *noise.Session, func(), error)

// SessionRun drives an established session until it ends.
type SessionRun func(ctx context.Context, mc peer.MsgConn, sess *noise.Session) error

const reconnectBase = time.Second

// ReconnectLoop dials, runs the session, and on a clean transport drop dials
// again onto the same machine (tmux still holds the shell). It stops when ctx
// is cancelled or dial/run returns a hard error.
func ReconnectLoop(ctx context.Context, dial SessionDial, run SessionRun) error {
	backoff := reconnectBase
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mc, sess, cleanup, err := dial(ctx)
		if ctx.Err() != nil {
			if cleanup != nil {
				cleanup()
			}
			return ctx.Err()
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = reconnectBase
		err = run(ctx, mc, sess)
		if cleanup != nil {
			cleanup()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !isTransientDetach(err) {
			return err
		}
	}
}

func isTransientDetach(err error) bool {
	return err == nil || errors.Is(err, peer.ErrDataChannelClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

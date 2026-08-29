package client

import (
	"context"
	"errors"
	"fmt"
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

// ErrReconnectGaveUp: the loop stopped after MaxFailures consecutive attempts
// that never yielded a healthy session. Bounded like the web client's parked
// "tap to retry": a gone agent gets a clear exit, not an endless dial storm.
var ErrReconnectGaveUp = errors.New("gave up reconnecting")

// ReconnectNotify surfaces loop transitions for user-facing status lines.
// Every field is optional.
type ReconnectNotify struct {
	// OnReconnecting fires before each redial during an outage; attempt counts
	// from 1 per outage (it resets after a healthy session).
	OnReconnecting func(attempt int)
	// OnResumed fires when a session is live again, with the outage length —
	// measured drop-to-redialed, the number the NAT-matrix work (P2) reads.
	OnResumed func(outage time.Duration)
	// OnGaveUp fires once, right before the loop returns ErrReconnectGaveUp.
	OnGaveUp func(failures int, lastErr error)
}

// ReconnectPolicy bounds the loop. Zero values take the defaults. The failure
// accounting mirrors web/src/net/reconnect.js: `failures` counts consecutive
// attempts that did NOT yield a healthy session — a dial error or a flap (a
// session up for less than MinHealthy) burns budget and backs off; a healthy
// drop resets everything and redials promptly, because a long-lived session
// dropping is a normal event, not a failing endpoint.
type ReconnectPolicy struct {
	Base        time.Duration // first retry delay (default 1s)
	Cap         time.Duration // max retry delay (default 15s)
	MaxFailures int           // consecutive failures before giving up (default 7)
	MinHealthy  time.Duration // uptime below this is a flap (default 5s)
	Notify      ReconnectNotify

	now   func() time.Time                                 // test hook
	sleep func(ctx context.Context, d time.Duration) error // test hook
}

func (p *ReconnectPolicy) defaults() {
	if p.Base <= 0 {
		p.Base = time.Second
	}
	if p.Cap <= 0 {
		p.Cap = 15 * time.Second
	}
	if p.MaxFailures <= 0 {
		p.MaxFailures = 7
	}
	if p.MinHealthy <= 0 {
		p.MinHealthy = 5 * time.Second
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.sleep == nil {
		p.sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
}

// ReconnectLoop dials, runs the session, and on a clean transport drop dials
// again onto the same machine (tmux still holds the shell), with the default
// policy. It stops on ctx cancel, a hard error, or after the failure budget.
func ReconnectLoop(ctx context.Context, dial SessionDial, run SessionRun) error {
	return ReconnectLoopWith(ctx, ReconnectPolicy{}, dial, run)
}

// ReconnectLoopWith is ReconnectLoop under an explicit policy.
func ReconnectLoopWith(ctx context.Context, p ReconnectPolicy, dial SessionDial, run SessionRun) error {
	p.defaults()
	backoff := p.Base
	failures := 0
	attempt := 0         // redials within the current outage, for OnReconnecting
	var dropAt time.Time // when the current outage began; zero = first connect
	var lastErr error

	bumpBackoff := func() {
		if backoff < p.Cap {
			backoff *= 2
			if backoff > p.Cap {
				backoff = p.Cap
			}
		}
	}
	failed := func(err error) (giveUp bool, sleepErr error) {
		failures++
		if err != nil {
			lastErr = err
		}
		if failures >= p.MaxFailures {
			if p.Notify.OnGaveUp != nil {
				p.Notify.OnGaveUp(failures, lastErr)
			}
			return true, nil
		}
		if serr := p.sleep(ctx, backoff); serr != nil {
			return false, serr
		}
		bumpBackoff()
		return false, nil
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !dropAt.IsZero() {
			attempt++
			if p.Notify.OnReconnecting != nil {
				p.Notify.OnReconnecting(attempt)
			}
		}
		mc, sess, cleanup, err := dial(ctx)
		if ctx.Err() != nil {
			if cleanup != nil {
				cleanup()
			}
			return ctx.Err()
		}
		if err != nil {
			giveUp, serr := failed(err)
			if giveUp {
				return fmt.Errorf("%w after %d attempts: %v", ErrReconnectGaveUp, failures, lastErr)
			}
			if serr != nil {
				return serr
			}
			continue
		}
		if !dropAt.IsZero() && p.Notify.OnResumed != nil {
			p.Notify.OnResumed(p.now().Sub(dropAt))
		}
		startedAt := p.now()
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
		dropAt = p.now()
		if dropAt.Sub(startedAt) >= p.MinHealthy {
			// A healthy session dropped: normal life. Reset the budget and
			// redial promptly (no sleep) so the resume feels instant.
			failures = 0
			attempt = 0
			backoff = p.Base
			lastErr = nil
			continue
		}
		// A flap: connected but died young. Burn budget and back off so a
		// half-up agent cannot hold the client in a dial storm.
		giveUp, serr := failed(err)
		if giveUp {
			if lastErr != nil {
				return fmt.Errorf("%w after %d attempts: %v", ErrReconnectGaveUp, failures, lastErr)
			}
			return fmt.Errorf("%w after %d attempts", ErrReconnectGaveUp, failures)
		}
		if serr != nil {
			return serr
		}
	}
}

func isTransientDetach(err error) bool {
	return err == nil || errors.Is(err, peer.ErrDataChannelClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

// go/internal/client/attach.go
package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// relayHeadStart is how long the relay locator waits before it starts, giving the
// LAN locator a head start. On the LAN, LAN-direct connects in tens of ms, so it
// wins inside this window and the relay is never contacted (a successful LAN attach
// stays relay-free — no relay round-trip, no metadata). When there is no LAN answer
// the relay starts after this delay, so a remote attach pays only ~this much rather
// than the full LAN budget. See dialStaggered.
const relayHeadStart = 200 * time.Millisecond

// kkHandshakeTimeout bounds Noise KK on one locator so a silent or wrong-key
// peer cannot stall attach; the race then continues to the next locator.
const kkHandshakeTimeout = 2 * time.Second

// postDialAuth runs after a locator returns a live MsgConn. A nil session with a
// nil error is a transport-only win (unit tests of the race). Any error,
// including KK failure or timeout, is treated as unreachable so a later locator
// can still win.
type postDialAuth func(ctx context.Context, mc peer.MsgConn, m Machine, id *Identity) (*noise.Session, error)

// Attach connects to the named machine's agent over the first locator that can
// complete transport AND Noise KK against the pinned host key. A locator that
// only opens a socket (spoofed LAN, wrong host key) is not a win: KK failure or
// timeout is unreachable and a later locator is still attempted.
func Attach(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer, relayOnly bool) (mc peer.MsgConn, sess *noise.Session, cleanup func(), err error) {
	if !id.HasRootedIdentity() {
		return nil, nil, nil, fmt.Errorf("this identity predates Miranda identity v2; run `mir identity rotate --yes` and re-pair")
	}
	hostPub, err := decodeHostPub(m.HostPubHex)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bad host pubkey for %q: %w", m.Name, err)
	}
	return dialStaggered(ctx, attachLocators(relayOnly), relayHeadStart, m, id, ice, kkAuth(hostPub))
}

func decodeHostPub(hexKey string) ([]byte, error) {
	pub, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, err
	}
	if len(pub) != 32 {
		return nil, fmt.Errorf("host pubkey must be 32 bytes, got %d", len(pub))
	}
	return pub, nil
}

func kkAuth(hostPub []byte) postDialAuth {
	return func(ctx context.Context, mc peer.MsgConn, m Machine, id *Identity) (*noise.Session, error) {
		hctx, cancel := context.WithTimeout(ctx, kkHandshakeTimeout)
		defer cancel()
		sess, err := peer.RunInitiator(hctx, mc, id.OwnerPriv(), hostPub)
		if err != nil {
			return nil, fmt.Errorf("%w: noise handshake: %v", ErrUnreachable, err)
		}
		return sess, nil
	}
}

// attachLocators is the ordered locator list Attach tries: LAN-direct first (a
// bounded mDNS+QUIC attempt) then the relay, unless relayOnly skips LAN.
func attachLocators(relayOnly bool) []Locator {
	if relayOnly {
		return []Locator{relayLocator{}}
	}
	return []Locator{lanLocator{res: newMDNSResolver()}, relayLocator{}}
}

// dialStaggered races locators "happy-eyeballs" style. A live MsgConn is not a
// win until postDialAuth succeeds (production: Noise KK against the pinned host
// key). Auth failure or timeout is unreachable: the conn is cleaned up and a
// later locator can still win. nil auth means transport-only (unit tests).
func dialStaggered(parent context.Context, locators []Locator, headStart time.Duration, m Machine, id *Identity, ice []peer.ICEServer, auth postDialAuth) (peer.MsgConn, *noise.Session, func(), error) {
	if len(locators) == 0 {
		return nil, nil, nil, fmt.Errorf("machine %q: no locators", m.Name)
	}
	if len(locators) == 1 {
		return completeLocator(parent, locators[0], m, id, ice, auth)
	}

	type dialResult struct {
		mc      peer.MsgConn
		sess    *noise.Session
		cleanup func()
		err     error
		i       int
	}
	results := make(chan dialResult, len(locators))
	cancels := make([]context.CancelFunc, len(locators))
	for i, loc := range locators {
		cctx, cancel := context.WithCancel(parent)
		cancels[i] = cancel
		go func(i int, loc Locator, cctx context.Context) {
			if i > 0 {
				select {
				case <-time.After(time.Duration(i) * headStart):
				case <-cctx.Done():
					results <- dialResult{err: context.Canceled, i: i}
					return
				}
			}
			mc, sess, cleanup, err := completeLocator(cctx, loc, m, id, ice, auth)
			results <- dialResult{mc, sess, cleanup, err, i}
		}(i, loc, cctx)
	}

	var bestErr error
	for pending := len(locators); pending > 0; pending-- {
		r := <-results
		if r.err == nil && r.mc != nil {
			for j := range cancels {
				if j != r.i {
					cancels[j]()
				}
			}
			remaining := pending - 1
			go func() {
				for ; remaining > 0; remaining-- {
					lr := <-results
					if lr.mc != nil && lr.cleanup != nil {
						lr.cleanup()
					}
				}
			}()
			winnerCancel := cancels[r.i]
			return r.mc, r.sess, func() {
				if r.cleanup != nil {
					r.cleanup()
				}
				winnerCancel()
			}, nil
		}
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			if bestErr == nil || (errors.Is(bestErr, ErrUnreachable) && !errors.Is(r.err, ErrUnreachable)) {
				bestErr = r.err
			}
		}
	}
	for _, c := range cancels {
		c()
	}
	if bestErr == nil {
		if parent.Err() != nil {
			return nil, nil, nil, parent.Err()
		}
		bestErr = fmt.Errorf("machine %q unreachable", m.Name)
	}
	return nil, nil, nil, bestErr
}

func completeLocator(ctx context.Context, loc Locator, m Machine, id *Identity, ice []peer.ICEServer, auth postDialAuth) (peer.MsgConn, *noise.Session, func(), error) {
	mc, cleanup, err := loc.Dial(ctx, m, id, ice)
	if err != nil {
		return nil, nil, nil, err
	}
	if auth == nil {
		return mc, nil, cleanup, nil
	}
	sess, err := auth(ctx, mc, m, id)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		if errors.Is(err, ErrUnreachable) {
			return nil, nil, nil, err
		}
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return mc, sess, cleanup, nil
}

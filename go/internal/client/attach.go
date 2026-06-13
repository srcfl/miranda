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

// Attach connects to the named machine's agent over the first locator that can
// reach it, runs the Noise KK initiator over that MsgConn, and returns the
// established session. Call cleanup when done.
//
// By default it tries LAN-direct (mDNS + QUIC) first, then falls back to the
// relay; the LAN attempt is bounded (see lanLocator.Dial) so a remote attach
// drops to the relay fast. relayOnly skips LAN entirely.
func Attach(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer, relayOnly bool) (mc peer.MsgConn, sess *noise.Session, cleanup func(), err error) {
	if !id.HasWallet() {
		return nil, nil, nil, fmt.Errorf("this identity has no wallet; run `mir keygen --wallet`")
	}

	mc, cleanup, err = dialStaggered(ctx, attachLocators(relayOnly), relayHeadStart, m, id, ice)
	if err != nil {
		return nil, nil, nil, err
	}

	hostPub, err := hex.DecodeString(m.HostPubHex)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("bad host pubkey for %q: %w", m.Name, err)
	}
	sess, err = peer.RunInitiator(ctx, mc, id.OwnerPriv(), hostPub)
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("noise handshake (wrong key / not paired?): %w", err)
	}
	return mc, sess, cleanup, nil
}

// attachLocators is the ordered locator list Attach tries: LAN-direct first (a
// bounded mDNS+QUIC attempt) then the relay, unless relayOnly skips LAN.
func attachLocators(relayOnly bool) []Locator {
	if relayOnly {
		return []Locator{relayLocator{}}
	}
	return []Locator{lanLocator{res: newMDNSResolver()}, relayLocator{}}
}

// dialStaggered races the locators "happy-eyeballs" style: locator[0] starts
// immediately and each later locator starts after an additional headStart, so a
// LAN that answers wins before the relay is ever contacted. The FIRST locator to
// return a live MsgConn wins; the others are cancelled and any that also connected
// is cleaned up. If all fail it returns the most informative error (a real failure
// in preference to ErrUnreachable). A single locator (relay-only) dials directly.
func dialStaggered(parent context.Context, locators []Locator, headStart time.Duration, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	if len(locators) == 0 {
		return nil, nil, fmt.Errorf("machine %q: no locators", m.Name)
	}
	if len(locators) == 1 {
		return locators[0].Dial(parent, m, id, ice)
	}

	type dialResult struct {
		mc      peer.MsgConn
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
			if i > 0 { // stagger later locators; cancellation pre-empts the wait
				select {
				case <-time.After(time.Duration(i) * headStart):
				case <-cctx.Done():
					results <- dialResult{err: context.Canceled, i: i}
					return
				}
			}
			mc, cleanup, err := loc.Dial(cctx, m, id, ice)
			results <- dialResult{mc, cleanup, err, i}
		}(i, loc, cctx)
	}

	var bestErr error
	for pending := len(locators); pending > 0; pending-- {
		r := <-results
		if r.err == nil && r.mc != nil {
			// Winner. Cancel the losers (keep the winner's ctx alive until its
			// session ends), and drain+close any loser that also connected.
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
			return r.mc, func() {
				if r.cleanup != nil {
					r.cleanup()
				}
				winnerCancel()
			}, nil
		}
		// Track the best error: a real failure beats ErrUnreachable / cancellation.
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
			return nil, nil, parent.Err()
		}
		bestErr = fmt.Errorf("machine %q unreachable", m.Name)
	}
	return nil, nil, bestErr
}

// go/internal/client/attach.go
package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Attach connects to the named machine's agent over the first locator that can
// reach it, runs the Noise KK initiator over that MsgConn, and returns the
// established session. Call cleanup when done.
func Attach(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (mc peer.MsgConn, sess *noise.Session, cleanup func(), err error) {
	if !id.HasWallet() {
		return nil, nil, nil, fmt.Errorf("this identity has no wallet; run `mir keygen --wallet`")
	}

	mc, cleanup, err = dialFirst([]Locator{relayLocator{}}, ctx, m, id, ice)
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

// dialFirst tries each locator in order, falling through on ErrUnreachable and
// aborting on any other (real) error. It returns the MsgConn from the first
// locator that connects, or the last ErrUnreachable (or a generic "unreachable"
// error) if none did.
func dialFirst(locators []Locator, ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	var lastErr error
	for _, loc := range locators {
		mc, cleanup, err := loc.Dial(ctx, m, id, ice)
		if errors.Is(err, ErrUnreachable) {
			lastErr = err
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		return mc, cleanup, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("machine %q unreachable", m.Name)
	}
	return nil, nil, lastErr
}

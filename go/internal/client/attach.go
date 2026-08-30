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

// kkHandshakeTimeout bounds Noise KK so a silent or wrong-key peer cannot
// stall attach.
const kkHandshakeTimeout = 2 * time.Second

// postDialAuth runs after a locator returns a live MsgConn. A nil session with a
// nil error is a transport-only win (unit tests). Any error, including KK
// failure or timeout, means the machine is unreachable by this path.
type postDialAuth func(ctx context.Context, mc peer.MsgConn, m Machine, id *Identity) (*noise.Session, error)

// Attach connects to the named machine's agent. One transport, two ICE modes:
// direct when candidates can pair (host candidates on a shared LAN, srflx
// across NATs) and TURN when they cannot — the ICE agent inside the relay
// locator prefers direct pairs on its own. A locator that only opens a socket
// (wrong host key) is not a win: the attach succeeds only after Noise KK
// against the pinned host key.
func Attach(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (mc peer.MsgConn, sess *noise.Session, cleanup func(), err error) {
	if !id.HasRootedIdentity() {
		return nil, nil, nil, fmt.Errorf("this identity predates Miranda identity v2; run `mir identity rotate --yes` and re-pair")
	}
	hostPub, err := decodeHostPub(m.HostPubHex)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bad host pubkey for %q: %w", m.Name, err)
	}
	return dialLocator(ctx, relayLocator{}, m, id, ice, kkAuth(hostPub))
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

// dialLocator dials one locator and gates the win on postDialAuth (production:
// Noise KK against the pinned host key). Auth failure cleans the conn up and
// reports unreachable. The Locator seam stays even though only the relay
// locator exists today: the mesh track (federated relays, DHT, offline local
// signaling) plugs in here without touching anything above it.
func dialLocator(ctx context.Context, loc Locator, m Machine, id *Identity, ice []peer.ICEServer, auth postDialAuth) (peer.MsgConn, *noise.Session, func(), error) {
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

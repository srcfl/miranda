// go/internal/peer/handshake.go
package peer

import (
	"context"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// RunInitiator runs the Plan-1 Noise_KK initiator over a DataChannel and returns
// the established encrypted session. staticPriv is the local static key; peerPub
// is the pinned remote static key (set at pairing).
func RunInitiator(ctx context.Context, mc MsgConn, staticPriv, peerPub []byte) (*noise.Session, error) {
	hs, err := noise.NewInitiator(staticPriv, peerPub)
	if err != nil {
		return nil, err
	}
	msg0, err := hs.WriteMessage(nil)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(msg0); err != nil {
		return nil, err
	}
	resp, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := hs.ReadMessage(resp); err != nil {
		return nil, err
	}
	return hs.Session(), nil
}

// RunResponder runs the Noise_KK responder over a DataChannel.
func RunResponder(ctx context.Context, mc MsgConn, staticPriv, peerPub []byte) (*noise.Session, error) {
	hs, err := noise.NewResponder(staticPriv, peerPub)
	if err != nil {
		return nil, err
	}
	msg0, err := mc.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := hs.ReadMessage(msg0); err != nil {
		return nil, err
	}
	resp, err := hs.WriteMessage(nil)
	if err != nil {
		return nil, err
	}
	if err := mc.Send(resp); err != nil {
		return nil, err
	}
	return hs.Session(), nil
}

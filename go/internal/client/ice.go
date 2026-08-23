package client

import (
	"context"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

const defaultSTUN = "stun:stun.l.google.com:19302"

// ResolveICE builds the ICE list for an attach. Ephemeral TURN from signalURL
// wins when offered: a TURN server already yields a server-reflexive candidate,
// so a third-party STUN server is omitted. stunFallback is used only when TURN
// is absent; empty means host candidates only.
func ResolveICE(ctx context.Context, signalURL string, stunFallback []string) ([]peer.ICEServer, error) {
	if strings.TrimSpace(signalURL) != "" {
		turn, err := peer.FetchTURN(ctx, signalURL)
		if err != nil {
			return nil, err
		}
		if len(turn) > 0 {
			return turn, nil
		}
	}
	if len(stunFallback) == 0 {
		return nil, nil
	}
	return []peer.ICEServer{{URLs: append([]string(nil), stunFallback...)}}, nil
}

// DefaultSTUN is the last-resort STUN URL when the relay offers no TURN.
func DefaultSTUN() string { return defaultSTUN }

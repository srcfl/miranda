// go/internal/client/attach.go
package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// Attach connects to the signaling server as the owner, negotiates a P2P
// DataChannel with the named machine's agent, runs the Noise KK initiator, and
// returns the established session. Call cleanup when done.
func Attach(ctx context.Context, m Machine, id *Identity, stun []string) (mc *peer.DataChannel, sess *noise.Session, cleanup func(), err error) {
	ownerPubHex := id.OwnerPubHex
	wsURL := "ws" + strings.TrimPrefix(m.SignalURL, "http") +
		"/attach?owner_id=" + url.QueryEscape(ownerPubHex) +
		"&machine_id=" + url.QueryEscape(m.MachineID)

	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial signaling: %w", err)
	}
	closeWS := func() { _ = c.CloseNow() }

	off, opened, err := peer.NewOfferer(stun)
	if err != nil {
		closeWS()
		return nil, nil, nil, err
	}
	cleanup = func() { _ = off.Close(); closeWS() }

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP})
	if err := c.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	var ans signal.SignalMsg
	if json.Unmarshal(data, &ans) != nil || ans.Type != signal.TypeAnswer {
		cleanup()
		if ans.Type == signal.TypeError {
			return nil, nil, nil, fmt.Errorf("signaling: %s", ans.Reason)
		}
		return nil, nil, nil, fmt.Errorf("unexpected signaling reply: %s", string(data))
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	octx, ocancel := context.WithTimeout(ctx, 20*time.Second)
	defer ocancel()
	select {
	case mc = <-opened:
	case <-octx.Done():
		cleanup()
		return nil, nil, nil, fmt.Errorf("no direct P2P path to %q (strict P2P, no relay fallback)", m.Name)
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

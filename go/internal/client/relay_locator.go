// go/internal/client/relay_locator.go
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/signal"
)

// relayLocator reaches a machine through the mir-signal relay: it dials the
// /attach WebSocket, exchanges SDP offer/answer, and waits for the WebRTC
// DataChannel to open. This is today's relay path, moved out of Attach verbatim.
type relayLocator struct{}

func (relayLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	ownerID := id.WalletAddress
	wsURL := "ws" + strings.TrimPrefix(m.SignalURL, "http") +
		"/attach?owner_id=" + url.QueryEscape(ownerID) +
		"&machine_id=" + url.QueryEscape(m.MachineID)

	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial signaling: %w", err)
	}
	closeWS := func() { _ = c.CloseNow() }

	off, opened, err := peer.NewOfferer(ice)
	if err != nil {
		closeWS()
		return nil, nil, err
	}
	cleanup := func() { _ = off.Close(); closeWS() }

	offerSDP, err := peer.CreateOffer(off)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	offerMsg, _ := json.Marshal(signal.SignalMsg{Type: signal.TypeOffer, SDP: offerSDP, Binding: id.BindingJSON})
	if err := c.Write(ctx, websocket.MessageText, offerMsg); err != nil {
		cleanup()
		return nil, nil, err
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	var ans signal.SignalMsg
	if json.Unmarshal(data, &ans) != nil || ans.Type != signal.TypeAnswer {
		cleanup()
		if ans.Type == signal.TypeError {
			return nil, nil, fmt.Errorf("signaling: %s", ans.Reason)
		}
		return nil, nil, fmt.Errorf("unexpected signaling reply: %s", string(data))
	}
	if err := peer.AcceptAnswer(off, ans.SDP); err != nil {
		cleanup()
		return nil, nil, err
	}

	octx, ocancel := context.WithTimeout(ctx, 20*time.Second)
	defer ocancel()
	var dc *peer.DataChannel
	select {
	case dc = <-opened:
	case <-octx.Done():
		cleanup()
		return nil, nil, fmt.Errorf("no direct P2P path to %q (strict P2P, no relay fallback)", m.Name)
	}

	return dc, cleanup, nil
}

// go/internal/pairing/wsconn.go
package pairing

import (
	"context"

	"github.com/coder/websocket"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// wsConn adapts a coder/websocket connection to peer.MsgConn (binary messages),
// so the NNpsk0 handshake can run over the signaling /pair channel.
type wsConn struct{ c *websocket.Conn }

// DialPair connects to the signaling server's /pair room and returns it as a MsgConn.
func DialPair(ctx context.Context, signalURL, roomID string) (peer.MsgConn, func(), error) {
	wsURL := toWS(signalURL) + "/pair?room=" + roomID
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, err
	}
	return &wsConn{c: c}, func() { _ = c.CloseNow() }, nil
}

func (w *wsConn) Send(b []byte) error {
	return w.c.Write(context.Background(), websocket.MessageBinary, b)
}

func (w *wsConn) Recv(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	return data, err
}

// toWS rewrites http(s):// to ws(s)://.
func toWS(base string) string {
	if len(base) >= 4 && base[:4] == "http" {
		return "ws" + base[4:]
	}
	return base
}

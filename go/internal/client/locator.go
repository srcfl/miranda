// go/internal/client/locator.go
package client

import (
	"context"
	"errors"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// ErrUnreachable means this locator can't reach the machine; Attach falls through
// to the next locator. Any other error aborts (a real failure on a reachable path).
var ErrUnreachable = errors.New("locator: machine not reachable by this path")

// Locator turns a Machine into a live MsgConn (post-transport, pre-Noise) plus a
// cleanup. Attach races locators and only treats a Dial as a win after Noise-KK
// against the pinned host key succeeds for that connection.
type Locator interface {
	Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error)
}

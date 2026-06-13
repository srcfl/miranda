// go/internal/client/lan_locator.go
package client

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/quicmsg"
)

// resolver maps a machine_id to a dialable host:port on the LAN. mdnsResolver is the
// prod impl; tests inject a static resolver so the QUIC/Noise path runs without
// multicast (flaky in CI). resolve returns ErrUnreachable when the machine isn't found.
type resolver interface {
	resolve(ctx context.Context, machineID string) (addr string, err error)
}

// lanLocator reaches an agent on the local network: it resolves the machine_id
// to a host:port (mDNS in prod, injectable in tests), QUIC-dials it, and sends
// the wallet binding as the first application frame before Noise-KK runs inside
// the MsgConn. It returns ErrUnreachable on any miss (no wallet, resolve miss,
// dial/send failure) so Attach falls through to the relay path.
type lanLocator struct{ res resolver }

func (l lanLocator) Dial(ctx context.Context, m Machine, id *Identity, _ []peer.ICEServer) (peer.MsgConn, func(), error) {
	if !id.HasWallet() {
		return nil, nil, ErrUnreachable // LAN attach requires a wallet binding
	}
	addr, err := l.res.resolve(ctx, m.MachineID)
	if err != nil {
		return nil, nil, ErrUnreachable
	}
	conn, err := quicmsg.Dial(ctx, addr)
	if err != nil {
		return nil, nil, ErrUnreachable
	}
	if err := conn.Send([]byte(id.BindingJSON)); err != nil { // frame 0: the wallet binding
		_ = conn.Close()
		return nil, nil, ErrUnreachable
	}
	return conn, func() { _ = conn.Close() }, nil
}

// mDNS service/domain the agent advertises on. The agent registers under
// _miranda._udp.local. with a "mid=<machine_id>" TXT entry (and/or the
// machine_id as the instance name) so the client can resolve it by machine_id.
const mdnsService = "_miranda._udp"
const mdnsDomain = "local."

// resolveTimeout bounds the mDNS browse so a miss fails fast (ErrUnreachable)
// rather than blocking Attach's first locator indefinitely.
var resolveTimeout = 1500 * time.Millisecond

// mdnsResolver is the production resolver: it browses the LAN over mDNS for the
// Miranda service and matches the requested machine_id.
type mdnsResolver struct{}

func (mdnsResolver) resolve(ctx context.Context, machineID string) (string, error) {
	r, err := zeroconf.NewResolver()
	if err != nil {
		return "", ErrUnreachable
	}

	// Bound the browse so a miss fails fast. Derive from the caller's ctx so
	// cancellation still propagates.
	bctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	entries := make(chan *zeroconf.ServiceEntry, 8)
	if err := r.Browse(bctx, mdnsService, mdnsDomain, entries); err != nil {
		return "", ErrUnreachable
	}

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				// Channel closed without a match (browse ended).
				return "", ErrUnreachable
			}
			if entry == nil || !matchesMachine(entry, machineID) {
				continue
			}
			if len(entry.AddrIPv4) == 0 || entry.Port == 0 {
				continue
			}
			return net.JoinHostPort(entry.AddrIPv4[0].String(), strconv.Itoa(entry.Port)), nil
		case <-bctx.Done():
			return "", ErrUnreachable
		}
	}
}

// matchesMachine reports whether a browsed service entry belongs to machineID,
// either via a "mid=<machine_id>" TXT record or by the instance name.
func matchesMachine(entry *zeroconf.ServiceEntry, machineID string) bool {
	if entry.Instance == machineID {
		return true
	}
	want := "mid=" + machineID
	for _, txt := range entry.Text {
		if strings.TrimSpace(txt) == want {
			return true
		}
	}
	return false
}

// newMDNSResolver returns the production mDNS-backed resolver.
func newMDNSResolver() resolver { return mdnsResolver{} }

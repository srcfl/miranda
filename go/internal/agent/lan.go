// go/internal/agent/lan.go
package agent

import (
	"context"
	"net"
	"strconv"

	"github.com/grandcat/zeroconf"

	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/quicmsg"
)

// lanService is the mDNS service type advertised for LAN-direct attach. Clients
// browse for this to discover an agent's ephemeral QUIC port on the local network.
const lanService = "_miranda._udp"

// startLAN opens a QUIC listener for LAN-direct attach and advertises it over mDNS.
// Returns the bound address (for callers/tests) and a stop func. Each connection runs
// the same binding-gated authenticated session as the relay path.
//
// The QUIC TLS identity carries no trust (see quicmsg): authentication is the wallet
// binding (frame 0) plus the Noise-KK handshake that run inside the stream.
func (rt *Runtime) startLAN(ctx context.Context) (addr string, stop func(), err error) {
	ln, err := quicmsg.Listen("0.0.0.0:0") // ephemeral; advertised via mDNS
	if err != nil {
		return "", nil, err
	}
	port, err := portOf(ln.Addr())
	if err != nil {
		_ = ln.Close()
		return "", nil, err
	}
	srv, err := zeroconf.Register(rt.cfg.MachineID, lanService, "local.", port, []string{"mid=" + rt.cfg.MachineID}, nil)
	if err != nil {
		_ = ln.Close()
		return "", nil, err
	}
	go rt.acceptLAN(ctx, ln)
	stop = func() {
		srv.Shutdown()
		_ = ln.Close()
	}
	// The listener binds 0.0.0.0 (all interfaces) so real LAN peers reach it via
	// the mDNS-advertised host IP. The returned addr is for local callers/tests, so
	// hand back a dialable loopback form rather than the unspecified 0.0.0.0/[::].
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), stop, nil
}

// portOf extracts the UDP port from a listener address. quic's listener returns a
// *net.UDPAddr, but we fall back to parsing the string form so we don't depend on
// the concrete type.
func portOf(a net.Addr) (int, error) {
	if ua, ok := a.(*net.UDPAddr); ok {
		return ua.Port, nil
	}
	_, portStr, err := net.SplitHostPort(a.String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portStr)
}

// acceptLAN loops accepting LAN-direct connections until the listener closes (stop)
// or ctx is done. Each connection is handled on its own goroutine.
func (rt *Runtime) acceptLAN(ctx context.Context, ln *quicmsg.Listener) {
	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			return // listener closed or ctx done
		}
		go rt.lanAccept(ctx, conn)
	}
}

// lanAccept gates a single LAN-direct connection: it reads the wallet binding as
// frame 0, refuses any unpinned wallet *before* the Noise handshake, recovers the
// X25519 pin from the binding, then runs the same authenticated PTY session as the
// relay path. admit() bounds concurrent pre-auth handshakes (a DoS bound shared with
// the relay path).
func (rt *Runtime) lanAccept(ctx context.Context, conn *quicmsg.Conn) {
	defer conn.Close()
	if !rt.admit() {
		return // too many pre-auth handshakes in flight
	}
	defer rt.release()

	bindingJSON, err := conn.Recv(ctx) // frame 0
	if err != nil {
		return
	}
	sb, err := identity.ParseSignedBinding(bindingJSON)
	if err != nil {
		return
	}
	if !rt.cfg.IsOwnerPinned(sb.Wallet) {
		return // unpinned wallet: refuse pre-Noise, no session starts
	}
	ownerPub, err := ownerPubFromBinding(string(bindingJSON), sb.Wallet)
	if err != nil {
		return
	}
	_ = rt.serveAuthenticated(ctx, conn, ownerPub)
}

// Package quicmsg is a QUIC-backed transport that satisfies peer.MsgConn,
// shared by the client (LAN dial) and the agent (LAN listen). Messages are
// length-framed over a single bidirectional QUIC stream.
//
// QUIC's TLS is treated as *dumb transport* here: it gives us an encrypted,
// reliable, ordered byte stream and nothing more. The real authentication —
// proving the peer is the right owner/agent — is the Noise-KK handshake plus
// the wallet binding that run *inside* this MsgConn. Because the transport TLS
// identity carries no trust, the client deliberately skips TLS verification
// (ClientTLS sets InsecureSkipVerify) and the server presents an ephemeral
// self-signed certificate (ServerTLS). Trust is established one layer up, not
// here.
package quicmsg

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"

	quic "github.com/quic-go/quic-go"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// Conn implements peer.MsgConn — the seam shared with the WebRTC DataChannel.
var _ peer.MsgConn = (*Conn)(nil)

// ALPN identifies the Miranda LAN-direct QUIC protocol. Both peers must agree
// on it during the TLS handshake.
const ALPN = "miranda/lan/v1"

// maxFrame bounds a single message to 1 MiB so a malicious or buggy peer can't
// make us allocate an absurd buffer from an attacker-controlled length prefix.
const maxFrame = 1 << 20

// Conn wraps a QUIC connection plus its single bidirectional stream and
// implements peer.MsgConn (Send/Recv). Messages are framed with a 4-byte
// big-endian length prefix.
type Conn struct {
	conn   *quic.Conn
	stream *quic.Stream
}

// Send writes a single length-prefixed frame: a 4-byte big-endian length
// followed by the payload. An empty payload is a valid frame (length 0).
func (c *Conn) Send(b []byte) error {
	if len(b) > maxFrame {
		return fmt.Errorf("quicmsg: frame too large: %d > %d", len(b), maxFrame)
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(b)))
	copy(buf[4:], b)
	if _, err := c.stream.Write(buf); err != nil {
		return err
	}
	return nil
}

// Recv reads exactly one length-prefixed frame. It honors ctx: if ctx is
// cancelled (or already cancelled) while blocked on the stream read, Recv
// returns promptly with ctx.Err() instead of parking forever.
//
// ctx is honored via the stream read deadline, mirroring how peer.DataChannel
// unblocks Recv. A watcher goroutine sets a past read deadline on ctx.Done,
// which makes any in-flight Read return a timeout error; on the normal path the
// watcher is torn down (and the deadline cleared) before returning, so no
// goroutine leaks.
func (c *Conn) Recv(ctx context.Context) (_ []byte, err error) {
	// Fast path: already-cancelled ctx shouldn't even touch the stream.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			// Force any blocked Read to return with a deadline-exceeded error.
			_ = c.stream.SetReadDeadline(time.Now().Add(-time.Second))
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-done
		// Clear the deadline so the next Recv isn't poisoned. Only meaningful
		// when the read completed normally; harmless otherwise.
		_ = c.stream.SetReadDeadline(time.Time{})
		// If the read failed because ctx fired, surface ctx.Err() rather than
		// the opaque deadline error.
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()

	var lenBuf [4]byte
	if _, err = io.ReadFull(c.stream, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrame {
		return nil, fmt.Errorf("quicmsg: incoming frame too large: %d > %d", n, maxFrame)
	}
	if n == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(c.stream, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Close closes the stream and the underlying QUIC connection.
func (c *Conn) Close() error {
	if c.stream != nil {
		_ = c.stream.Close()
	}
	if c.conn != nil {
		return c.conn.CloseWithError(0, "")
	}
	return nil
}

// ServerTLS returns a TLS config for the listener using a freshly generated,
// ephemeral self-signed certificate. The cert identity is meaningless on
// purpose: trust comes from Noise-KK + the wallet binding inside the stream,
// not from this certificate.
func ServerTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "miranda-lan"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN},
	}, nil
}

// ClientTLS returns a TLS config for dialing. It skips verification on purpose:
// the QUIC TLS identity carries no trust in Miranda — the real authentication
// is the Noise-KK handshake and wallet binding that run inside the stream.
func ClientTLS() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // trust is established by Noise-KK inside, not by TLS
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{ALPN},
	}
}

// Dial connects to a quicmsg listener at addr, opens the single bidirectional
// stream, and wraps it in a *Conn. On any error the QUIC connection is closed.
func Dial(ctx context.Context, addr string) (*Conn, error) {
	conn, err := quic.DialAddr(ctx, addr, ClientTLS(), nil)
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	// Nudge the stream open so the listener's AcceptStream returns without
	// waiting for the first payload. Writing the empty frame keeps both sides
	// in lockstep on the framing protocol.
	if _, err := stream.Write([]byte{0, 0, 0, 0}); err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	return &Conn{conn: conn, stream: stream}, nil
}

// Listener wraps a QUIC listener and yields *Conn for each accepted connection.
type Listener struct {
	ln *quic.Listener
}

// Listen binds a quicmsg listener at addr (e.g. "127.0.0.1:0" for an ephemeral
// port). Use Addr to discover the bound address.
func Listen(addr string) (*Listener, error) {
	tlsConf, err := ServerTLS()
	if err != nil {
		return nil, err
	}
	ln, err := quic.ListenAddr(addr, tlsConf, nil)
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln}, nil
}

// Addr returns the address the listener is bound to (so callers/mDNS can learn
// the ephemeral port).
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Accept waits for the next QUIC connection, accepts its first bidirectional
// stream, consumes the open-nudge frame Dial sent, and wraps it in a *Conn.
func (l *Listener) Accept(ctx context.Context) (*Conn, error) {
	conn, err := l.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	c := &Conn{conn: conn, stream: stream}
	// Consume the empty open-nudge frame Dial wrote to flush the stream open.
	if _, err := c.Recv(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the listener (does not close already-accepted connections).
func (l *Listener) Close() error { return l.ln.Close() }

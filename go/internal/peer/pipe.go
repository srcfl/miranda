// go/internal/peer/pipe.go
package peer

import "context"

// Pipe returns two connected in-memory MsgConns (like net.Pipe but
// message-oriented). For tests that need a DataChannel-shaped conn without WebRTC.
func Pipe() (MsgConn, MsgConn) {
	a2b := make(chan []byte, 64)
	b2a := make(chan []byte, 64)
	return &memConn{in: b2a, out: a2b}, &memConn{in: a2b, out: b2a}
}

type memConn struct {
	in  <-chan []byte
	out chan<- []byte
}

func (m *memConn) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	m.out <- cp
	return nil
}

func (m *memConn) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-m.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

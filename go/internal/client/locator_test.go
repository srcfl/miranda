// go/internal/client/locator_test.go
package client

import (
	"context"
	"errors"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// fakeConn is a no-op MsgConn used to prove dialFirst returns the conn from the
// first locator that connects.
type fakeConn struct{}

func (fakeConn) Send(b []byte) error                      { return nil }
func (fakeConn) Recv(ctx context.Context) ([]byte, error) { return nil, nil }

// stubLocator returns a canned (conn, cleanup, err) regardless of inputs and
// records whether it was invoked.
type stubLocator struct {
	conn    peer.MsgConn
	cleanup func()
	err     error
	called  *bool
}

func (s stubLocator) Dial(ctx context.Context, m Machine, id *Identity, ice []peer.ICEServer) (peer.MsgConn, func(), error) {
	if s.called != nil {
		*s.called = true
	}
	return s.conn, s.cleanup, s.err
}

func TestDialFirstFallsThroughOnUnreachable(t *testing.T) {
	want := fakeConn{}
	secondCalled := false
	cleaned := false
	locators := []Locator{
		stubLocator{err: ErrUnreachable},
		stubLocator{conn: want, cleanup: func() { cleaned = true }, called: &secondCalled},
	}

	mc, cleanup, err := dialFirst(locators, context.Background(), Machine{Name: "box"}, &Identity{}, nil)
	if err != nil {
		t.Fatalf("dialFirst: unexpected error: %v", err)
	}
	if mc != want {
		t.Fatalf("dialFirst returned wrong conn: got %#v want %#v", mc, want)
	}
	if !secondCalled {
		t.Fatal("expected fall-through to the second locator")
	}
	// The returned cleanup must be the second locator's, not the first's.
	cleanup()
	if !cleaned {
		t.Fatal("expected the second locator's cleanup to be returned")
	}
}

func TestDialFirstAbortsOnRealError(t *testing.T) {
	boom := errors.New("boom: reachable path failed")
	secondCalled := false
	locators := []Locator{
		stubLocator{err: boom},
		stubLocator{conn: fakeConn{}, called: &secondCalled},
	}

	mc, _, err := dialFirst(locators, context.Background(), Machine{Name: "box"}, &Identity{}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the real error to abort, got: %v", err)
	}
	if mc != nil {
		t.Fatal("expected no conn on abort")
	}
	if secondCalled {
		t.Fatal("a real (non-unreachable) error must NOT fall through to the next locator")
	}
}

func TestDialFirstAllUnreachable(t *testing.T) {
	locators := []Locator{
		stubLocator{err: ErrUnreachable},
		stubLocator{err: ErrUnreachable},
	}

	mc, _, err := dialFirst(locators, context.Background(), Machine{Name: "box"}, &Identity{}, nil)
	if mc != nil {
		t.Fatal("expected no conn when all locators are unreachable")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable when every locator falls through, got: %v", err)
	}
}

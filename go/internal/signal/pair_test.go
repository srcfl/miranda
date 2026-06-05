// go/internal/signal/pair_test.go
package signal

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPairBridgeForwardsBothWays(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	ctx := context.Background()
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL(srv.URL, "/pair", map[string]string{"room": "abc"}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := dial()
	b := dial()

	if err := a.Write(ctx, websocket.MessageBinary, []byte("a->b")); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, got, err := b.Read(rctx)
	if err != nil || string(got) != "a->b" {
		t.Fatalf("b got %q err %v", got, err)
	}

	if err := b.Write(ctx, websocket.MessageBinary, []byte("b->a")); err != nil {
		t.Fatal(err)
	}
	_, got, err = a.Read(rctx)
	if err != nil || string(got) != "b->a" {
		t.Fatalf("a got %q err %v", got, err)
	}
}

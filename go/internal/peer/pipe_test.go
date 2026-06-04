// go/internal/peer/pipe_test.go
package peer

import (
	"context"
	"testing"
	"time"
)

func TestPipeCarriesMessagesBothWays(t *testing.T) {
	a, b := Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := a.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := b.Recv(ctx)
	if err != nil || string(got) != "ping" {
		t.Fatalf("b got %q err %v", got, err)
	}

	if err := b.Send([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	got, err = a.Recv(ctx)
	if err != nil || string(got) != "pong" {
		t.Fatalf("a got %q err %v", got, err)
	}
}

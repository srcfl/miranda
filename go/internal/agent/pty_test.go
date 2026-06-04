// go/internal/agent/pty_test.go
package agent

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPTYRunsShellCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := StartPTY(ctx, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if _, err := p.Write([]byte("echo terminal_relay_marker\n")); err != nil {
		t.Fatal(err)
	}

	// Read until we see the marker echoed by the shell.
	deadline := time.Now().Add(8 * time.Second)
	var acc bytes.Buffer
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		_ = p.SetReadDeadlineSoon()
		n, _ := p.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			if bytes.Contains(acc.Bytes(), []byte("terminal_relay_marker")) {
				return // success
			}
		}
	}
	t.Fatalf("marker never seen; got:\n%s", acc.String())
}

func TestPTYResizeDoesNotError(t *testing.T) {
	ctx := context.Background()
	p, err := StartPTY(ctx, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
}

// go/internal/agent/pty_test.go
package agent

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
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

// TestPTYCloseReapsChild verifies Close() reaps the child (calls Wait) so it
// does not become a zombie. After Close, cmd.ProcessState must be populated and
// the OS must no longer report the pid in the "Z" (zombie) state.
func TestPTYCloseReapsChild(t *testing.T) {
	ctx := context.Background()
	p, err := StartPTY(ctx, []string{"sh"})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.cmd.Process.Pid

	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Wait ran => ProcessState is populated (it is nil until reaped).
	if p.cmd.ProcessState == nil {
		t.Fatal("cmd.ProcessState is nil after Close: child was not reaped (Wait never ran)")
	}

	// The pid must not be lingering as a zombie in the OS process table.
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))
	if strings.HasPrefix(state, "Z") {
		t.Fatalf("pid %d is a zombie (state %q) after Close: child not reaped", pid, state)
	}
}

package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The PTY must set TERM (xterm.js client) so tmux/clear/vim work even when the
// agent is launched by launchd/systemd with no TERM in its environment.
func TestStartPTYSetsTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p, err := StartPTY(ctx, []string{"sh", "-c", "printf 'T=%s.' \"$TERM\""})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	var out []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = p.SetReadDeadlineSoon()
		n, _ := p.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if strings.Contains(string(out), "T=") && strings.Contains(string(out), ".") {
			break
		}
	}
	if !strings.Contains(string(out), "T=xterm-256color.") {
		t.Fatalf("PTY did not set TERM=xterm-256color; got %q", string(out))
	}
}

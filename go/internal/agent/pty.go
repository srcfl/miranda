// go/internal/agent/pty.go
package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
)

// PTY is a pseudo-terminal running a command (a shell, or tmux in production).
type PTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// StartPTY launches argv behind a PTY. Production passes
// {"tmux","new","-A","-s","main"}; tests pass {"sh"}.
func StartPTY(ctx context.Context, argv []string) (*PTY, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The client is xterm.js; set TERM so tmux/clear/vim work. Without this, an
	// agent launched by launchd/systemd (no TERM in its env) gives the child an
	// empty terminal type -> "terminal does not support clear".
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &PTY{f: f, cmd: cmd}, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.f.Write(b) }

// SetReadDeadlineSoon nudges a short read deadline so a polling read loop in a
// test does not block forever. Best-effort (ignored if unsupported).
func (p *PTY) SetReadDeadlineSoon() error {
	return p.f.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
}

func (p *PTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

func (p *PTY) Close() error {
	// Close the PTY fd first (unblocks any pending Read), then kill and reap the
	// child. Without Wait() the kernel keeps the process-table entry as a zombie
	// until the agent exits — leaking one PID per attach over the agent lifetime.
	_ = p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	return nil
}

// TmuxInstalled reports whether tmux is on PATH (checked before a real `up`).
func TmuxInstalled() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

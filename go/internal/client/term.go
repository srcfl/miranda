// go/internal/client/term.go
package client

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// AttachHint is the half of the attach banner every entry path shares: which
// machine you are on, and where Ctrl-C goes (to the shell — it is not a detach
// key). Each caller appends the way out it actually offers, so no banner can
// promise a gesture that does not exist there.
func AttachHint(machineName string) string {
	return fmt.Sprintf("attached to %s — Ctrl-C goes to the shell", machineName)
}

// RunInteractive puts the real terminal into raw mode, wires SIGWINCH to RESIZE,
// and runs the bridge against stdin/stdout. Restores the terminal on exit.
func RunInteractive(ctx context.Context, mc peer.MsgConn, sess *noise.Session, machineName string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("mir attach requires a TTY (stdin is not a terminal)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, old) }()
	// One attach, no picker behind it: closing the client is the way out, and
	// Ctrl-O then d belongs to the overview, so it is not claimed here.
	fmt.Fprintf(os.Stderr, "[mir] %s; close the client to detach (the session keeps running)\r\n", AttachHint(machineName))

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}

	resizes := make(chan Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			c, r, e := term.GetSize(fd)
			if e == nil {
				select {
				case resizes <- Size{Cols: uint16(c), Rows: uint16(r)}:
				default:
				}
			}
		}
	}()

	return ClientBridge(ctx, os.Stdin, os.Stdout, resizes, Size{Cols: uint16(cols), Rows: uint16(rows)}, mc, sess)
}

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

// RunInteractive puts the real terminal into raw mode, wires SIGWINCH to RESIZE,
// and runs the bridge against stdin/stdout. Restores the terminal on exit.
func RunInteractive(ctx context.Context, mc *peer.DataChannel, sess *noise.Session, machineName string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("mir attach requires a TTY (stdin is not a terminal)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, old) }()
	fmt.Fprintf(os.Stderr, "[mir] attached to %s — Ctrl-C goes to the shell; close the client to detach\r\n", machineName)

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

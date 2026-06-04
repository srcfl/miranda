// go/internal/client/muxterm.go
package client

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

// AttachAll attaches every named machine and returns their sessions + a cleanup.
// On any failure it cleans up the ones already attached.
func AttachAll(ctx context.Context, dir string, names []string, id *Identity, ice []peer.ICEServer) ([]*MuxSession, func(), error) {
	var sessions []*MuxSession
	var cleanups []func()
	cleanupAll := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, name := range names {
		m, err := GetMachine(dir, name)
		if err != nil {
			cleanupAll()
			return nil, nil, err
		}
		mc, sess, cleanup, err := Attach(ctx, *m, id, ice)
		if err != nil {
			cleanupAll()
			return nil, nil, fmt.Errorf("attach %s: %w", name, err)
		}
		sessions = append(sessions, &MuxSession{Name: m.Name, MC: mc, Sess: sess})
		cleanups = append(cleanups, cleanup)
	}
	return sessions, cleanupAll, nil
}

// RunInteractiveMux puts the terminal in raw mode and runs the mux over sessions.
func RunInteractiveMux(ctx context.Context, sessions []*MuxSession) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("tr attach requires a TTY (stdin is not a terminal)")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, old) }()

	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	fmt.Fprintf(os.Stderr, "[tr] %d machines: %v — switch with Ctrl-] then 1-9 / n / q\r\n", len(sessions), names)

	resizes := make(chan Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if c, r, e := term.GetSize(fd); e == nil {
				select {
				case resizes <- Size{Cols: uint16(c), Rows: uint16(r)}:
				default:
				}
			}
		}
	}()

	mux := NewMux(sessions, os.Stdout, DefaultPrefix, Size{Cols: uint16(cols), Rows: uint16(rows)})
	return mux.Run(ctx, os.Stdin, resizes)
}

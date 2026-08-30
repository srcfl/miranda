// go/internal/cli/overview.go — `mir` with no arguments opens your machines
// (spec 2026-08-30, decision D3). A picker, not a multiplexer: rows of text, a
// cursor, and keys on the raw terminal — tmux keeps owning windows and panes,
// the mux keeps owning machine focus, this screen owns where you start.
//
// The overview process is also the warm place: the pre-attach caches (T2) are
// primed on entry and refreshed while it is open, so Enter feels instant. One
// live attach at a time in v1 — Ctrl-O then d comes back here.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

const (
	overviewRefresh = 3 * time.Second // cheap: T2's cache answers most ticks
	overviewPrefix  = 0x0f            // Ctrl-O, same default as the mux
)

const (
	altScreenOn  = "\x1b[?1049h\x1b[?25l"
	altScreenOff = "\x1b[?25h\x1b[?1049l"
	clearScreen  = "\x1b[H\x1b[2J"
)

// cmdOverview runs the interactive machine overview. Callers have already
// checked that stdin and stdout are terminals.
func (a *app) cmdOverview() error {
	if freshSetup() {
		// A brand-new user gets the welcome walkthrough, not a keychain prompt
		// from identity creation and an empty screen.
		a.guide()
		return nil
	}
	dir := defaultClientDir()
	idn, err := a.identity(dir)
	if err != nil {
		return err
	}
	if !idn.HasRootedIdentity() {
		// A legacy identity cannot read the registry; the guide carries the
		// one-time migration instructions.
		a.guide()
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(fd, oldState) }()
	fmt.Fprint(a.out, altScreenOn)
	defer fmt.Fprint(a.out, altScreenOff)

	pump := newStdinPump()
	ov := &overviewState{
		app:  a,
		dir:  dir,
		idn:  idn,
		pump: pump,
		model: &overviewModel{
			Binary: a.binary,
			Status: "loading your machines…",
		},
		windows: map[string]string{},
	}
	ov.resize(fd)
	ov.draw()
	ov.refresh(ctx, true)
	ov.draw()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	tick := time.NewTicker(overviewRefresh)
	defer tick.Stop()

	var dec keyDecoder
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-winch:
			ov.resize(fd)
			ov.draw()
		case <-tick.C:
			if ov.refresh(ctx, false) {
				ov.draw()
			}
		case chunk, ok := <-pump.ch:
			if !ok {
				return nil
			}
			for _, b := range chunk {
				done, err := ov.handleKey(ctx, dec.feed(b))
				if err != nil {
					return err
				}
				if done {
					return nil
				}
			}
		}
	}
}

// overviewState is the loop's mutable world: the model it renders, the caches
// behind it, and the machines the rows point at.
type overviewState struct {
	app      *app
	dir      string
	idn      *client.Identity
	pump     *stdinPump
	model    *overviewModel
	machines []client.Machine  // row i -> machines[i]
	fresh    map[string]bool   // machine ids first seen while this overview is up
	windows  map[string]string // machine name -> last tmux summary line
	iceList  []peer.ICEServer
	prompt   promptKind
	input    []byte
}

type promptKind int

const (
	promptNone promptKind = iota
	promptRename
	promptRetire
)

func (ov *overviewState) resize(fd int) {
	if w, h, err := term.GetSize(fd); err == nil {
		ov.model.Width, ov.model.Height = w, h
	}
}

func (ov *overviewState) draw() {
	fmt.Fprint(ov.app.out, clearScreen+ov.model.Render())
}

// refresh reloads the machine list through the T2 caches. Reports whether the
// rows changed. The first call also primes TURN credentials so Enter is warm,
// and pins the NEW badge set for the lifetime of this overview.
func (ov *overviewState) refresh(ctx context.Context, first bool) bool {
	turnURL := ""
	if first {
		turnURL = defaults.SignalURL()
	}
	warm := ov.app.prewarm(ctx, ov.dir, ov.idn, turnURL, nil)
	if first && warm.ICEErr == nil && len(warm.ICE) > 0 {
		ov.iceList = warm.ICE
	}
	local, err := client.ListMachines(ov.dir)
	if err != nil {
		ov.model.Status = "could not read local machines: " + err.Error()
		return true
	}
	revocations, err := client.ListRevocations(ov.dir)
	if err != nil {
		ov.model.Status = "could not read revocations: " + err.Error()
		return true
	}
	discovered := client.FilterRevoked(warm.Discovered, ov.idn.OwnerID, revocations)
	merged := client.FilterRevoked(client.MergeMachines(local, discovered), ov.idn.OwnerID, revocations)
	if first {
		// Badge machines the seen-set does not know, then record them so the
		// next overview run starts clean. The badge itself stays for this run.
		ov.fresh = client.UnseenMachines(ov.dir, discovered)
		_ = client.NotifyNewDevices(io.Discard, ov.dir, discovered)
	}
	online := map[string]bool{}
	for _, m := range discovered {
		online[m.MachineID] = true
	}

	selected := ""
	if row, ok := ov.model.Selected(); ok {
		selected = row.Name
	}
	rows := make([]overviewRow, 0, len(merged))
	for _, m := range merged {
		rows = append(rows, overviewRow{
			Name:        m.Name,
			MachineID:   m.MachineID,
			Online:      online[m.MachineID],
			New:         ov.fresh[m.MachineID],
			WindowsLine: ov.windows[m.Name],
		})
	}
	changed := first || !rowsEqual(ov.model.Rows, rows)
	ov.machines = merged
	ov.model.Rows = rows
	// Keep the cursor on the row the user had selected; on first load, start
	// on the last-used machine — Enter then means "continue where I left off".
	target := selected
	if first {
		target = client.LastUsed(ov.dir)
	}
	if target != "" {
		for i, r := range rows {
			if r.Name == target {
				ov.model.Cursor = i
				break
			}
		}
	}
	ov.model.MoveCursor(0)
	if warm.RegistryErr != nil {
		ov.model.Status = discoveryPausedNote
	} else if ov.model.Status == "loading your machines…" || ov.model.Status == discoveryPausedNote {
		ov.model.Status = ""
	}
	return changed
}

func rowsEqual(a, b []overviewRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleKey applies one decoded key. done=true quits the overview cleanly.
func (ov *overviewState) handleKey(ctx context.Context, ev keyEvent) (done bool, err error) {
	if ov.prompt != promptNone {
		ov.handlePromptKey(ctx, ev)
		ov.draw()
		return false, nil
	}
	switch ev.Key {
	case ovQuit:
		return true, nil
	case ovUp:
		ov.model.MoveCursor(-1)
	case ovDown:
		ov.model.MoveCursor(1)
	case ovHelp:
		ov.model.Status = ovHelpLine
	case ovEnter:
		if i := ov.model.Cursor; i >= 0 && i < len(ov.machines) {
			if err := ov.attach(ctx, ov.machines[i]); err != nil {
				return false, err
			}
		}
	case ovRename:
		if row, ok := ov.model.Selected(); ok {
			ov.prompt = promptRename
			ov.input = nil
			ov.model.Prompt = "new name for " + row.Name + ": "
			ov.model.Input = ""
		}
	case ovRetire:
		if row, ok := ov.model.Selected(); ok {
			ov.prompt = promptRetire
			ov.input = nil
			ov.model.Prompt = "Retire " + row.Name + "? It disappears from every device; the machine and its tmux keep running; `" +
				ov.app.binary + " up` + pair brings it back. [y/N] "
			ov.model.Input = ""
		}
	}
	ov.draw()
	return false, nil
}

// handlePromptKey drives the one-line inline prompt (rename text / retire y-N).
func (ov *overviewState) handlePromptKey(ctx context.Context, ev keyEvent) {
	switch ev.Key {
	case ovEsc, ovQuit:
		if ev.Rune == 'q' { // q is ordinary text inside a prompt
			ov.input = append(ov.input, ev.Rune)
			ov.model.Input = string(ov.input)
			return
		}
		ov.closePrompt("nothing changed")
	case ovEnter:
		kind, text := ov.prompt, string(ov.input)
		ov.closePrompt("")
		switch kind {
		case promptRename:
			ov.finishRename(ctx, text)
		case promptRetire:
			ov.finishRetire(ctx, text)
		}
	case ovRune, ovUp, ovDown, ovRename, ovRetire, ovHelp:
		b := ev.Rune
		if b == 0x7f || b == 0x08 { // backspace
			if len(ov.input) > 0 {
				ov.input = ov.input[:len(ov.input)-1]
			}
		} else if b >= 0x20 && b < 0x7f {
			ov.input = append(ov.input, b)
		}
		ov.model.Input = string(ov.input)
	}
}

func (ov *overviewState) closePrompt(status string) {
	ov.prompt = promptNone
	ov.input = nil
	ov.model.Prompt = ""
	ov.model.Input = ""
	ov.model.Status = status
}

func (ov *overviewState) finishRename(ctx context.Context, newName string) {
	i := ov.model.Cursor
	if newName == "" || i < 0 || i >= len(ov.machines) {
		ov.model.Status = "nothing changed"
		return
	}
	m := ov.machines[i]
	if err := ov.app.renameResolved(ctx, ov.dir, ov.idn, m, newName, ov.ice()); err != nil {
		ov.model.Status = err.Error()
		return
	}
	ov.model.Status = fmt.Sprintf("✓ renamed %q → %q", m.Name, newName)
	if ov.windows[m.Name] != "" {
		ov.windows[newName] = ov.windows[m.Name]
	}
	ov.refresh(ctx, false)
}

func (ov *overviewState) finishRetire(ctx context.Context, answer string) {
	i := ov.model.Cursor
	if i < 0 || i >= len(ov.machines) {
		return
	}
	switch answer {
	case "y", "Y", "yes":
	default:
		ov.model.Status = "nothing changed"
		return
	}
	m := ov.machines[i]
	if err := ov.app.retireResolved(ctx, io.Discard, ov.dir, ov.idn, m); err != nil {
		ov.model.Status = err.Error()
		return
	}
	ov.model.Status = fmt.Sprintf("⊘ retired %q — to use it again: run `%s up` on it and pair fresh", m.Name, ov.app.binary)
	ov.refresh(ctx, false)
}

func (ov *overviewState) ice() []peer.ICEServer {
	if len(ov.iceList) > 0 {
		return ov.iceList
	}
	if s := splitCSV(defaults.STUNURL()); len(s) > 0 {
		return []peer.ICEServer{{URLs: s}}
	}
	return nil
}

// attach leaves the overview screen, runs the machine's terminal with the R1
// reconnect loop, and returns here when the user detaches (Ctrl-O then d) or
// the loop gives up. The stdin pump keeps running; the bridge reads from it
// through the detach filter.
func (ov *overviewState) attach(ctx context.Context, m client.Machine) error {
	a := ov.app
	fmt.Fprint(a.out, altScreenOff)
	fmt.Fprintf(os.Stderr, "[%s] attached to %s — Ctrl-O then d comes back to your machines\r\n", a.binary, m.Name)

	attachCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := newDetachFilter(ov.pump, overviewPrefix, cancel)

	sink := func(payload []byte) {
		if line := windowsSummary(payload); line != "" {
			ov.windows[m.Name] = line
		}
	}
	var once sync.Once
	err := client.ReconnectLoopWith(attachCtx, client.ReconnectPolicy{Notify: attachNotify(a, m.Name)},
		func(ctx context.Context) (peer.MsgConn, *noise.Session, func(), error) {
			return client.Attach(ctx, m, ov.idn, ov.ice())
		},
		func(ctx context.Context, mc peer.MsgConn, sess *noise.Session) error {
			once.Do(func() { client.SaveLastUsed(ov.dir, m.Name) })
			return runBridgePumped(ctx, mc, sess, in, sink)
		})

	fmt.Fprint(a.out, altScreenOn)
	ov.model.Status = ""
	if err != nil && attachCtx.Err() == nil && ctx.Err() == nil && !isCleanDetach(err) {
		ov.model.Status = humanAttachErr(a.binary, m.Name, err).Error()
	}
	if row, ok := ov.model.Selected(); ok && row.Name == m.Name {
		ov.model.Rows[ov.model.Cursor].WindowsLine = ov.windows[m.Name]
	}
	ov.draw()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// attachNotify is the same honest status voice cmdAttach uses, shared so the
// overview's attach does not drift from `mir attach`'s.
func attachNotify(a *app, name string) client.ReconnectNotify {
	return client.ReconnectNotify{
		OnReconnecting: func(attempt int) {
			if attempt == 1 {
				fmt.Fprintf(a.errOut, "\r\n[%s] connection lost — reconnecting…\r\n", a.binary)
			} else {
				fmt.Fprintf(a.errOut, "[%s] still reconnecting (attempt %d)…\r\n", a.binary, attempt)
			}
		},
		OnResumed: func(outage time.Duration) {
			fmt.Fprintf(a.errOut, "[%s] reconnected in %.1fs\r\n", a.binary, outage.Seconds())
		},
		OnGaveUp: func(failures int, lastErr error) {
			fmt.Fprintf(a.errOut, "\r\n[%s] is the machine up? Check `%s list`, then rerun `%s attach %s`.\r\n", a.binary, a.binary, a.binary, name)
		},
	}
}

// runBridgePumped is RunInteractive for a terminal that is already raw and a
// stdin that is already owned by the overview's pump: it only wires size +
// SIGWINCH and runs the bridge.
func runBridgePumped(ctx context.Context, mc peer.MsgConn, sess *noise.Session, in io.Reader, sink client.WindowsSink) error {
	fd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	resizes := make(chan client.Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if c, r, e := term.GetSize(fd); e == nil {
				select {
				case resizes <- client.Size{Cols: uint16(c), Rows: uint16(r)}:
				default:
				}
			}
		}
	}()
	return client.ClientBridgeSink(ctx, in, os.Stdout, resizes,
		client.Size{Cols: uint16(cols), Rows: uint16(rows)}, mc, sess, sink)
}

// stdinPump owns os.Stdin for the whole overview session: one goroutine reads,
// everyone else consumes the channel. That is what lets the overview and an
// attach hand the keyboard back and forth without ever re-opening stdin.
type stdinPump struct {
	ch chan []byte
}

func newStdinPump() *stdinPump {
	p := &stdinPump{ch: make(chan []byte, 8)}
	go func() {
		defer close(p.ch)
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				p.ch <- chunk
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

// detachFilter feeds pump bytes to the bridge, watching for prefix+d — the
// local "back to your machines" gesture. Prefix twice sends the prefix byte
// through (same convention as the mux); any other follow-up sends both bytes.
// On detach it cancels the attach context FIRST, then reports EOF, so the
// reconnect loop sees a cancelled context, not a droppped link to redial.
type detachFilter struct {
	pump   *stdinPump
	prefix byte
	detach func()
	buf    []byte
	armed  bool
	done   bool
}

func newDetachFilter(pump *stdinPump, prefix byte, detach func()) *detachFilter {
	return &detachFilter{pump: pump, prefix: prefix, detach: detach}
}

func (f *detachFilter) Read(p []byte) (int, error) {
	for {
		if f.done {
			return 0, io.EOF
		}
		if len(f.buf) > 0 {
			n := copy(p, f.buf)
			f.buf = f.buf[n:]
			return n, nil
		}
		chunk, ok := <-f.pump.ch
		if !ok {
			return 0, io.EOF
		}
		out := make([]byte, 0, len(chunk)+1)
		for _, b := range chunk {
			if f.armed {
				f.armed = false
				switch b {
				case 'd', 'q':
					f.detach()
					f.done = true
					if len(out) > 0 {
						f.buf = out
					}
					// Deliver what came before the gesture, then EOF.
					if len(f.buf) > 0 {
						n := copy(p, f.buf)
						f.buf = f.buf[n:]
						return n, nil
					}
					return 0, io.EOF
				case f.prefix:
					out = append(out, f.prefix)
				default:
					out = append(out, f.prefix, b)
				}
				continue
			}
			if b == f.prefix {
				f.armed = true
				continue
			}
			out = append(out, b)
		}
		f.buf = out
	}
}

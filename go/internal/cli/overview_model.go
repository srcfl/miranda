// go/internal/cli/overview_model.go — the pure half of the `mir` overview:
// rows, cursor, key decoding, and rendering to a string. No terminal, no
// clock, no network — overview.go owns those — so everything here tests
// against plain buffers, the way web's pool/grace modules do.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/client"
)

// ovKey is one decoded overview key press.
type ovKey int

const (
	ovNone ovKey = iota
	ovUp
	ovDown
	ovEnter
	ovRename
	ovRetire
	ovQuit
	ovHelp
	ovEsc
	ovShare
	ovRune // an ordinary byte; Rune carries it (prompt input)
)

// keyEvent pairs a decoded key with its raw byte for prompt input.
type keyEvent struct {
	Key  ovKey
	Rune byte
}

// keyDecoder turns raw bytes into overview keys. Arrow keys arrive as ESC [ A/B
// and can be split across reads, so the decoder keeps that little state.
type keyDecoder struct {
	esc int // 0 = idle, 1 = saw ESC, 2 = saw ESC [
}

// feed decodes one byte. ovNone means "swallowed, waiting for the rest of an
// escape sequence".
func (d *keyDecoder) feed(b byte) keyEvent {
	switch d.esc {
	case 1:
		if b == '[' {
			d.esc = 2
			return keyEvent{Key: ovNone}
		}
		d.esc = 0
		return keyEvent{Key: ovEsc}
	case 2:
		d.esc = 0
		switch b {
		case 'A':
			return keyEvent{Key: ovUp}
		case 'B':
			return keyEvent{Key: ovDown}
		}
		return keyEvent{Key: ovNone}
	}
	switch b {
	case 0x1b:
		d.esc = 1
		return keyEvent{Key: ovNone}
	case '\r', '\n':
		return keyEvent{Key: ovEnter}
	case 'j':
		return keyEvent{Key: ovDown, Rune: b}
	case 'k':
		return keyEvent{Key: ovUp, Rune: b}
	case 'q', 0x03: // q / Ctrl-C
		return keyEvent{Key: ovQuit, Rune: b}
	case 'r':
		return keyEvent{Key: ovRename, Rune: b}
	case 'x':
		return keyEvent{Key: ovRetire, Rune: b}
	case 's':
		return keyEvent{Key: ovShare, Rune: b}
	case '?':
		return keyEvent{Key: ovHelp, Rune: b}
	}
	return keyEvent{Key: ovRune, Rune: b}
}

// overviewRow is one machine as the overview shows it.
type overviewRow struct {
	Name        string
	MachineID   string
	Online      bool
	New         bool   // discovered for the first time while this overview is up
	Shared      bool   // a share someone gave this identity (guest entry)
	WindowsLine string // dim one-line tmux summary or share detail; "" hides the line
}

// overviewModel is everything the overview renders. The loop mutates it and
// calls Render; tests do the same with a fake size.
type overviewModel struct {
	Binary string
	Rows   []overviewRow
	Cursor int
	Status string // transient line above the hint bar ("" hides it)
	Prompt string // active inline prompt label ("" = none)
	Input  string // what the prompt has collected so far
	Width  int
	Height int
}

const (
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiReset  = "\x1b[0m"
	ovHintBar  = "enter attach · s share · r rename · x retire · q quit · ? help"
	ovHelpLine = "↑/↓ or j/k move · enter attaches · s shares (owner) · r renames · x retires (asks first) · q quits"
)

// MoveCursor moves the selection, clamped to the row list.
func (m *overviewModel) MoveCursor(delta int) {
	m.Cursor += delta
	if m.Cursor < 0 {
		m.Cursor = 0
	}
	if m.Cursor >= len(m.Rows) {
		m.Cursor = len(m.Rows) - 1
	}
	if m.Cursor < 0 {
		m.Cursor = 0
	}
}

// Selected returns the row under the cursor.
func (m *overviewModel) Selected() (overviewRow, bool) {
	if m.Cursor < 0 || m.Cursor >= len(m.Rows) {
		return overviewRow{}, false
	}
	return m.Rows[m.Cursor], true
}

// Render draws the whole overview as raw-mode text (\r\n line endings). The
// caller clears the screen and writes the result.
func (m *overviewModel) Render() string {
	var b strings.Builder
	line := func(s string) {
		if m.Width > 0 && visibleLen(s) > m.Width {
			s = truncateVisible(s, m.Width)
		}
		b.WriteString(s)
		b.WriteString("\r\n")
	}
	line("")
	line(" " + ansiBold + m.Binary + ansiReset + ansiDim + " — your machines" + ansiReset)
	line("")
	if len(m.Rows) == 0 {
		line(" no machines yet — run `" + m.Binary + " up` on the machine you want to reach;")
		line(" its first run shows a pairing QR")
	}
	for i, r := range m.Rows {
		cursor := "  "
		if i == m.Cursor {
			cursor = " ▸"
		}
		state := "○"
		if r.Online {
			state = "●"
		}
		if r.Shared {
			state = "⇢" // a share: its grant, not the registry, is its state
		}
		badge := ""
		if r.New {
			badge = "  " + ansiBold + "NEW" + ansiReset
		}
		line(fmt.Sprintf("%s %s %s%s", cursor, state, r.Name, badge))
		if r.WindowsLine != "" {
			line("     " + ansiDim + r.WindowsLine + ansiReset)
		}
	}
	line("")
	if m.Prompt != "" {
		line(" " + m.Prompt + m.Input + "▏")
	} else if m.Status != "" {
		line(" " + m.Status)
	}
	line(" " + ansiDim + ovHintBar + ansiReset)
	return b.String()
}

// visibleLen counts characters that occupy a cell (ANSI escapes do not).
func visibleLen(s string) int {
	n, esc := 0, false
	for _, r := range s {
		if esc {
			if r == 'm' {
				esc = false
			}
			continue
		}
		if r == 0x1b {
			esc = true
			continue
		}
		n++
	}
	return n
}

// truncateVisible cuts s to width visible cells, keeping escapes intact and
// closing any open style.
func truncateVisible(s string, width int) string {
	var b strings.Builder
	n, esc := 0, false
	for _, r := range s {
		if esc {
			b.WriteRune(r)
			if r == 'm' {
				esc = false
			}
			continue
		}
		if r == 0x1b {
			esc = true
			b.WriteRune(r)
			continue
		}
		if n >= width {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String() + ansiReset
}

// windowsSummary renders the agent's tmux snapshot (FrameWindows v2 JSON) as
// one dim line: "3 windows — claude, build, logs". "" when there is nothing
// worth a line. The client-side mirror of agent/windows.go's sessSnapshot.
func windowsSummary(payload []byte) string {
	var snap struct {
		Sess []struct {
			N   string `json:"n"`
			Act bool   `json:"act"`
			Win []struct {
				N string `json:"n"`
			} `json:"win"`
		} `json:"sess"`
	}
	if err := json.Unmarshal(payload, &snap); err != nil || len(snap.Sess) == 0 {
		return ""
	}
	sess := snap.Sess[0]
	for _, s := range snap.Sess {
		if s.Act {
			sess = s
			break
		}
	}
	if len(sess.Win) == 0 {
		return ""
	}
	names := make([]string, 0, 4)
	for _, w := range sess.Win {
		if len(names) == 4 {
			names = append(names, "…")
			break
		}
		names = append(names, w.N)
	}
	noun := "windows"
	if len(sess.Win) == 1 {
		noun = "window"
	}
	return fmt.Sprintf("%d %s — %s", len(sess.Win), noun, strings.Join(names, ", "))
}

// pickDefaultMachine resolves what a bare `mir attach` means: the last-used
// machine when it still exists, else the only machine there is.
func pickDefaultMachine(lastUsed string, machines []client.Machine) (client.Machine, bool) {
	for _, m := range machines {
		if lastUsed != "" && m.Name == lastUsed {
			return m, true
		}
	}
	if len(machines) == 1 {
		return machines[0], true
	}
	return client.Machine{}, false
}

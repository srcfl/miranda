// go/internal/cli/overview_test.go — the pure half of the overview: key
// decoding (split escape sequences included), rendering, the windows summary,
// the bare-attach default, and the detach filter's gesture handling.
package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/client"
)

func feedAll(t *testing.T, d *keyDecoder, bytes []byte) []keyEvent {
	t.Helper()
	var evs []keyEvent
	for _, b := range bytes {
		if ev := d.feed(b); ev.Key != ovNone {
			evs = append(evs, ev)
		}
	}
	return evs
}

func TestKeyDecoderBasics(t *testing.T) {
	var d keyDecoder
	cases := []struct {
		in   []byte
		want []ovKey
	}{
		{[]byte("j"), []ovKey{ovDown}},
		{[]byte("k"), []ovKey{ovUp}},
		{[]byte("\r"), []ovKey{ovEnter}},
		{[]byte("q"), []ovKey{ovQuit}},
		{[]byte{0x03}, []ovKey{ovQuit}},
		{[]byte("r"), []ovKey{ovRename}},
		{[]byte("x"), []ovKey{ovRetire}},
		{[]byte("?"), []ovKey{ovHelp}},
		{[]byte("\x1b[A"), []ovKey{ovUp}},
		{[]byte("\x1b[B"), []ovKey{ovDown}},
		{[]byte("z"), []ovKey{ovRune}},
	}
	for _, c := range cases {
		evs := feedAll(t, &d, c.in)
		if len(evs) != len(c.want) {
			t.Fatalf("%q: got %d events, want %d", c.in, len(evs), len(c.want))
		}
		for i, ev := range evs {
			if ev.Key != c.want[i] {
				t.Fatalf("%q: event %d = %v, want %v", c.in, i, ev.Key, c.want[i])
			}
		}
	}
}

// Arrow-key escape sequences can arrive split across reads; the decoder must
// hold its state between bytes.
func TestKeyDecoderSplitEscape(t *testing.T) {
	var d keyDecoder
	if ev := d.feed(0x1b); ev.Key != ovNone {
		t.Fatalf("ESC alone should wait, got %v", ev.Key)
	}
	if ev := d.feed('['); ev.Key != ovNone {
		t.Fatalf("ESC [ should wait, got %v", ev.Key)
	}
	if ev := d.feed('A'); ev.Key != ovUp {
		t.Fatalf("ESC [ A = %v, want ovUp", ev.Key)
	}
	// A bare ESC followed by a normal byte is an escape (prompt cancel).
	if ev := d.feed(0x1b); ev.Key != ovNone {
		t.Fatalf("ESC alone should wait, got %v", ev.Key)
	}
	if ev := d.feed('x'); ev.Key != ovEsc {
		t.Fatalf("ESC x = %v, want ovEsc", ev.Key)
	}
}

func TestOverviewRender(t *testing.T) {
	m := &overviewModel{
		Binary: "mir",
		Rows: []overviewRow{
			{Name: "zap-dev", Online: true, New: true, WindowsLine: "3 windows — claude, build, logs"},
			{Name: "office-mini", Online: false},
		},
		Cursor: 0,
		Width:  100,
	}
	out := m.Render()
	for _, want := range []string{
		"your machines",
		"▸ ● zap-dev",
		"NEW",
		"3 windows — claude, build, logs",
		"  ○ office-mini",
		ovHintBar,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "\r\n") {
		t.Fatal("render must use raw-mode line endings")
	}
}

func TestOverviewRenderEmptyAndPrompt(t *testing.T) {
	m := &overviewModel{Binary: "mir", Width: 100}
	out := m.Render()
	if !strings.Contains(out, "no machines yet — run `mir up`") {
		t.Fatalf("empty state missing:\n%s", out)
	}
	m.Rows = []overviewRow{{Name: "box"}}
	m.Prompt = "new name for box: "
	m.Input = "workbench"
	out = m.Render()
	if !strings.Contains(out, "new name for box: workbench") {
		t.Fatalf("prompt line missing:\n%s", out)
	}
}

func TestMoveCursorClamps(t *testing.T) {
	m := &overviewModel{Rows: make([]overviewRow, 3)}
	m.MoveCursor(-5)
	if m.Cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.Cursor)
	}
	m.MoveCursor(99)
	if m.Cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.Cursor)
	}
}

func TestWindowsSummary(t *testing.T) {
	v2 := []byte(`{"v":2,"sess":[
		{"n":"other","act":false,"win":[{"n":"idle"}]},
		{"n":"main","act":true,"win":[{"n":"claude"},{"n":"build"},{"n":"logs"}]}]}`)
	if got := windowsSummary(v2); got != "3 windows — claude, build, logs" {
		t.Fatalf("summary = %q", got)
	}
	one := []byte(`{"v":2,"sess":[{"n":"main","act":true,"win":[{"n":"sh"}]}]}`)
	if got := windowsSummary(one); got != "1 window — sh" {
		t.Fatalf("summary = %q", got)
	}
	if got := windowsSummary([]byte(`not json`)); got != "" {
		t.Fatalf("bad JSON should render nothing, got %q", got)
	}
	if got := windowsSummary([]byte(`{"v":2,"sess":[]}`)); got != "" {
		t.Fatalf("no sessions should render nothing, got %q", got)
	}
}

func TestPickDefaultMachine(t *testing.T) {
	ms := []client.Machine{{Name: "a"}, {Name: "b"}}
	if m, ok := pickDefaultMachine("b", ms); !ok || m.Name != "b" {
		t.Fatalf("last-used pick = %v %v", m, ok)
	}
	if _, ok := pickDefaultMachine("gone", ms); ok {
		t.Fatal("two machines and no match must not pick")
	}
	if m, ok := pickDefaultMachine("", ms[:1]); !ok || m.Name != "a" {
		t.Fatalf("only-machine pick = %v %v", m, ok)
	}
	if _, ok := pickDefaultMachine("", nil); ok {
		t.Fatal("no machines must not pick")
	}
}

// The detach filter passes ordinary bytes through, sends a doubled prefix as
// one literal prefix, and turns prefix+d into: deliver what came before, cancel
// the attach, then EOF.
func TestDetachFilter(t *testing.T) {
	pump := &stdinPump{ch: make(chan []byte, 8)}
	detached := false
	f := newDetachFilter(pump, 0x0f, func() { detached = true })

	pump.ch <- []byte("hi")
	buf := make([]byte, 16)
	n, err := f.Read(buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("plain read = %q, %v", buf[:n], err)
	}

	pump.ch <- []byte{0x0f, 0x0f} // prefix twice = literal prefix
	n, err = f.Read(buf)
	if err != nil || string(buf[:n]) != "\x0f" {
		t.Fatalf("doubled prefix = %q, %v", buf[:n], err)
	}

	pump.ch <- []byte{0x0f, 'z'} // prefix + other = both bytes
	n, err = f.Read(buf)
	if err != nil || string(buf[:n]) != "\x0fz" {
		t.Fatalf("prefix+other = %q, %v", buf[:n], err)
	}

	pump.ch <- []byte{'a', 0x0f, 'd', 'x'} // bytes before the gesture come first
	n, err = f.Read(buf)
	if err != nil || string(buf[:n]) != "a" {
		t.Fatalf("pre-gesture bytes = %q, %v", buf[:n], err)
	}
	if !detached {
		t.Fatal("prefix+d must cancel the attach")
	}
	if _, err := f.Read(buf); err != io.EOF {
		t.Fatalf("after detach: err = %v, want EOF", err)
	}
}

// The gesture can be split across reads (prefix in one chunk, d in the next).
func TestDetachFilterSplitGesture(t *testing.T) {
	pump := &stdinPump{ch: make(chan []byte, 8)}
	detached := false
	f := newDetachFilter(pump, 0x0f, func() { detached = true })
	pump.ch <- []byte{0x0f}
	pump.ch <- []byte{'d'}
	buf := make([]byte, 4)
	if _, err := f.Read(buf); err != io.EOF {
		t.Fatalf("split gesture: err = %v, want EOF", err)
	}
	if !detached {
		t.Fatal("split gesture must still detach")
	}
}

func TestLastUsedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := client.LastUsed(dir); got != "" {
		t.Fatalf("empty dir: LastUsed = %q", got)
	}
	client.SaveLastUsed(dir, "zap-dev")
	if got := client.LastUsed(dir); got != "zap-dev" {
		t.Fatalf("LastUsed = %q, want zap-dev", got)
	}
	client.SaveLastUsed(dir, "office")
	if got := client.LastUsed(dir); got != "office" {
		t.Fatalf("LastUsed after overwrite = %q, want office", got)
	}
}

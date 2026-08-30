// go/internal/cli/argorder_test.go — the argument order our own help text
// documents must parse. Go's flag.FlagSet stops at the first positional, so
// `mir share box --ttl 1h` used to drop `--ttl 1h` into Args() and fail the
// arity check (#112, after the same trap hit `mir attach` in #45). These tests
// drive the real dispatch, one row per command that takes positionals AND
// flags, so the class cannot come back a third time.
package cli

import (
	"bytes"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

// TestParseArgsAcceptsAnyOrder pins the shared helper: flags before, after, and
// between positionals all land on the same values, and "--" ends flag parsing.
func TestParseArgsAcceptsAnyOrder(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantPos   []string
		wantTTL   time.Duration
		wantWrite bool
	}{
		{"flags first (Go's native order)", []string{"--ttl", "2h", "--write", "box"}, []string{"box"}, 2 * time.Hour, true},
		{"documented order", []string{"box", "--ttl", "2h", "--write"}, []string{"box"}, 2 * time.Hour, true},
		{"interleaved", []string{"--ttl", "2h", "box", "--write"}, []string{"box"}, 2 * time.Hour, true},
		{"single dash spelling", []string{"box", "-ttl", "2h"}, []string{"box"}, 2 * time.Hour, false},
		{"two positionals, trailing flag", []string{"box", "newbox", "--write"}, []string{"box", "newbox"}, time.Hour, true},
		{"two positionals, flag between", []string{"box", "--ttl", "2h", "newbox"}, []string{"box", "newbox"}, 2 * time.Hour, false},
		{"no positionals", nil, nil, time.Hour, false},
		{"terminator keeps dashes positional", []string{"--write", "box", "--", "-weird", "--weirder"}, []string{"box", "-weird", "--weirder"}, time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			ttl := fs.Duration("ttl", time.Hour, "")
			write := fs.Bool("write", false, "")
			got := parseArgs(fs, tc.args)
			if strings.Join(got, "|") != strings.Join(tc.wantPos, "|") {
				t.Errorf("positionals = %q, want %q", got, tc.wantPos)
			}
			if *ttl != tc.wantTTL {
				t.Errorf("--ttl = %v, want %v", *ttl, tc.wantTTL)
			}
			if *write != tc.wantWrite {
				t.Errorf("--write = %v, want %v", *write, tc.wantWrite)
			}
		})
	}
}

// TestDocumentedArgOrderParses drives every command that mixes positionals with
// flags. Each row runs the human-friendly documented order AND Go flag's native
// leading-flag order; both must get past parsing to the same real answer, and
// neither may come back with a usage line.
func TestDocumentedArgOrderParses(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	t.Setenv("MIR_SIGNAL", "http://127.0.0.1:1") // dead relay: discovery fails fast
	withShareTTY(t, false)
	withRevokeTTY(t, false)
	dir := t.TempDir()

	cases := []struct {
		name string
		// documented is the order our usage strings and help text show.
		documented []string
		// native is Go flag's own leading-flag order, which always worked.
		native []string
		// want is a fragment of the answer both orders must reach: an honest
		// refusal about the real argument, never a usage line.
		want string
	}{
		{
			name:       "attach",
			documented: []string{"attach", "box", "--dir", dir},
			native:     []string{"attach", "--dir", dir, "box"},
			want:       `unknown machine "box"`,
		},
		{
			name:       "attach several machines",
			documented: []string{"attach", "box", "other", "--dir", dir},
			native:     []string{"attach", "--dir", dir, "box", "other"},
			want:       `unknown machine "box"`,
		},
		{
			// run is the deliberate exception: everything after <machine> is
			// the remote command (`-n` below belongs to echo), so mir's own
			// flags come first. Both spellings here are that documented order.
			name:       "run",
			documented: []string{"run", "--dir", dir, "box", "echo", "-n", "hi"},
			native:     []string{"run", "--dir", dir, "--window", "1s", "box", "echo", "hi"},
			want:       `unknown machine "box"`,
		},
		{
			// The #112 bug, verbatim.
			name:       "share",
			documented: []string{"share", "box", "--ttl", "1h", "--dir", dir},
			native:     []string{"share", "--ttl", "1h", "--dir", dir, "box"},
			want:       "needs a person at the terminal",
		},
		{
			name:       "share revoke",
			documented: []string{"share", "revoke", "abcdef12", "--dir", dir},
			native:     []string{"share", "revoke", "--dir", dir, "abcdef12"},
			want:       `no share matches "abcdef12"`,
		},
		{
			name:       "join",
			documented: []string{"join", "not-a-code", "--dir", dir},
			native:     []string{"join", "--dir", dir, "not-a-code"},
			want:       "bad pairing code",
		},
		{
			name:       "pair",
			documented: []string{"pair", "not-a-code", "--dir", dir},
			native:     []string{"pair", "--dir", dir, "not-a-code"},
			want:       "bad pairing code",
		},
		{
			name:       "machine rename",
			documented: []string{"machine", "rename", "box", "newbox", "--dir", dir},
			native:     []string{"machine", "rename", "--dir", dir, "box", "newbox"},
			want:       `unknown machine "box"`,
		},
		{
			name:       "machine revoke",
			documented: []string{"machine", "revoke", "box", "--yes", "--dir", dir},
			native:     []string{"machine", "revoke", "--yes", "--dir", dir, "box"},
			want:       `unknown machine "box"`,
		},
	}

	for _, tc := range cases {
		for _, order := range []struct {
			label string
			argv  []string
		}{{"documented", tc.documented}, {"native", tc.native}} {
			t.Run(tc.name+"/"+order.label, func(t *testing.T) {
				var out, errb bytes.Buffer
				a := &app{in: strings.NewReader(""), out: &out, errOut: &errb, binary: "mir"}
				if code := a.run(order.argv); code == 0 {
					t.Fatalf("%v should have refused, stdout=%q", order.argv, out.String())
				}
				if strings.Contains(errb.String(), "usage:") {
					t.Fatalf("%v was refused with a usage line — the documented order must parse:\n%s", order.argv, errb.String())
				}
				if !strings.Contains(errb.String(), tc.want) {
					t.Fatalf("%v: stderr missing %q:\n%s", order.argv, tc.want, errb.String())
				}
			})
		}
	}
}

// Flag-only commands have nothing to interleave, but `identity show --dir X`
// is the shape users type most, so pin that it still works end to end.
func TestFlagOnlyCommandsUnchanged(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	t.Setenv("MIR_SIGNAL", "http://127.0.0.1:1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	a := &app{in: strings.NewReader(""), out: &out, errOut: &errb, binary: "mir"}
	if code := a.run([]string{"identity", "show", "--dir", dir}); code != 0 {
		t.Fatalf("identity show exit = %d, stderr = %q", code, errb.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("identity show printed no owner id")
	}
}

// A share still refuses without a terminal, whichever order the arguments come
// in: parsing was widened, the consent gate was not.
func TestShareStillRefusesWithoutTTY(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	t.Setenv("MIR_SIGNAL", "http://127.0.0.1:1")
	withShareTTY(t, false)
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"box", "--write", "--dir", dir},
		{"--write", "--dir", dir, "box"},
	} {
		var out bytes.Buffer
		a := &app{in: strings.NewReader("box\n"), out: &out, errOut: io.Discard, binary: "mir"}
		err := a.cmdShare(argv)
		if err == nil || !strings.Contains(err.Error(), "needs a person at the terminal") {
			t.Fatalf("share %v: err = %v, want the no-TTY refusal", argv, err)
		}
		if strings.Contains(out.String(), "Type the machine name") {
			t.Fatalf("share %v reached the write prompt before the TTY refusal:\n%s", argv, out.String())
		}
	}
}

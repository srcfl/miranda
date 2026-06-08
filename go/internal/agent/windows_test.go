// go/internal/agent/windows_test.go
package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseSessionsGroupsAndMarksActive covers the core of the multi-session
// overview: windows grouped by session in tmux's output order, the viewed
// session flagged, per-session active window, and the window flags carried through.
func TestParseSessionsGroupsAndMarksActive(t *testing.T) {
	in := strings.Join([]string{
		"main|@0|0|edit|1|0|0|nvim|2",
		"main|@1|1|shell|0|1|0|bash|1",
		"work|@7|0|logs|1|0|1|tail|1",
		"",
	}, "\n")

	b := parseSessions(in, "work")
	if b == nil {
		t.Fatal("nil snapshot")
	}
	var s sessSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.V != 2 {
		t.Fatalf("v = %d, want 2", s.V)
	}
	if len(s.Sess) != 2 {
		t.Fatalf("sessions = %d, want 2", len(s.Sess))
	}
	// order preserved: main then work
	if s.Sess[0].N != "main" || s.Sess[1].N != "work" {
		t.Fatalf("session order = %q, %q", s.Sess[0].N, s.Sess[1].N)
	}
	if s.Sess[0].Act {
		t.Error("main should not be the active session")
	}
	if !s.Sess[1].Act {
		t.Error("work should be the active session")
	}
	if len(s.Sess[0].Win) != 2 {
		t.Fatalf("main windows = %d, want 2", len(s.Sess[0].Win))
	}
	if s.Sess[0].AW != "@0" {
		t.Errorf("main active window = %q, want @0", s.Sess[0].AW)
	}
	if !s.Sess[0].Win[1].A {
		t.Error("main:1 should carry the activity flag")
	}
	w := s.Sess[1].Win[0]
	if !w.B {
		t.Error("work:0 should carry the bell flag")
	}
	if w.Cmd != "tail" || w.P != 1 || w.ID != "@7" {
		t.Errorf("work:0 = %+v", w)
	}
}

func TestParseSessionsEmpty(t *testing.T) {
	if parseSessions("", "main") != nil {
		t.Error("empty input should yield nil")
	}
	if parseSessions("   \n  \n", "main") != nil {
		t.Error("blank input should yield nil")
	}
}

func TestParseSessionsSkipsMalformedLines(t *testing.T) {
	in := "garbage\nmain|@0|0|edit|1|0|0|nvim|1\n"
	b := parseSessions(in, "main")
	var s sessSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Sess) != 1 || len(s.Sess[0].Win) != 1 {
		t.Fatalf("got %+v, want one session with one window", s.Sess)
	}
}

func TestValidSessTarget(t *testing.T) {
	for _, s := range []string{"main", "0", "my-proj", "a.b_c", "x"} {
		if !validSessTarget(s) {
			t.Errorf("%q should be a valid target", s)
		}
	}
	for _, s := range []string{"", "has:colon", "a\nb", "\x7f", strings.Repeat("x", 65)} {
		if validSessTarget(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

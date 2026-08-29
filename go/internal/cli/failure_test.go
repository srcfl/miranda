package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/client"
)

// N3's bar: a user-facing failure is a plain sentence, one next step, and the
// cause in parentheses — never the bare wrapped chain alone. These pin the
// high-traffic rewrites.

func TestHumanAttachErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"unreachable", fmt.Errorf("machine %q unreachable", "box"),
			[]string{`machine "box" is unreachable`, "start it with `mir up`", "`mir list`", "cause:"}},
		{"agent-unavailable", errors.New("signaling: agent unavailable"),
			[]string{`machine "box" is unreachable`, "cause: signaling: agent unavailable"}},
		{"gave-up", fmt.Errorf("%w after 7 attempts", client.ErrReconnectGaveUp),
			[]string{`machine "box" is unreachable`, "cause:"}},
		{"locator-unreachable", fmt.Errorf("%w: noise handshake: boom", client.ErrUnreachable),
			[]string{`machine "box" is unreachable`}},
		{"relay-down", errors.New(`dial signaling: Post "https://relay": connection refused`),
			[]string{"the relay is unreachable", "`mir doctor`", "cause:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanAttachErr("mir", "box", tc.err)
			for _, want := range tc.want {
				if !strings.Contains(got.Error(), want) {
					t.Fatalf("missing %q in: %s", want, got)
				}
			}
		})
	}
}

// An error with a plain cause of its own must pass through untouched — the
// taxonomy rewrites known causes, it does not blanket-wrap everything.
func TestHumanAttachErrPassthrough(t *testing.T) {
	orig := errors.New("mir attach requires a TTY (stdin is not a terminal)")
	if got := humanAttachErr("mir", "box", orig); got != orig {
		t.Fatalf("unrelated error was rewritten: %v", got)
	}
	if humanAttachErr("mir", "box", nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestHumanPairAndUpdateErr(t *testing.T) {
	pair := humanPairHandshakeErr(errors.New("pairing handshake failed (wrong code?): eof"))
	for _, want := range []string{"wrong or expired", "5 minutes", "get a fresh one", "cause:"} {
		if !strings.Contains(pair.Error(), want) {
			t.Fatalf("pair copy missing %q: %s", want, pair)
		}
	}
	up := humanUpdateErr(errors.New("github releases: 403 Forbidden"))
	for _, want := range []string{"could not update", "github.com/srcfl/miranda/releases", "cause:"} {
		if !strings.Contains(up.Error(), want) {
			t.Fatalf("update copy missing %q: %s", want, up)
		}
	}
}

func TestClockSkew(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	header := now.Add(-10 * time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	skew, ok := clockSkew(header, now)
	if !ok || skew < 9*time.Minute || skew > 11*time.Minute {
		t.Fatalf("skew = %v ok=%v, want ~10m", skew, ok)
	}
	if skew, ok := clockSkew(now.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), now); !ok || skew > time.Second {
		t.Fatalf("same-time skew = %v ok=%v", skew, ok)
	}
	if _, ok := clockSkew("", now); ok {
		t.Fatal("empty Date header must not report a skew")
	}
	if _, ok := clockSkew("not a date", now); ok {
		t.Fatal("garbage Date header must not report a skew")
	}
}

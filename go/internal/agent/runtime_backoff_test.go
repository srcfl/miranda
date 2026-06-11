package agent

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The reconnect backoff is UPTIME-GATED. A connection that stayed healthy long
// enough resets the backoff (prompt reconnect after a normal idle drop); a
// connection that the relay accepted then dropped before minHealthyUptime is a
// FLAP and must GROW the backoff so a crash-looping / ping-ponging relay can't
// drive a flat 1s reconnect storm. A failed dial also grows.
func TestNextBackoffUptimeGated(t *testing.T) {
	const (
		base = 1 * time.Second
		max  = 30 * time.Second
	)
	tests := []struct {
		name   string
		prev   time.Duration
		dialed bool
		uptime time.Duration
		want   time.Duration
	}{
		{
			name:   "healthy connection resets to base",
			prev:   8 * time.Second,
			dialed: true,
			uptime: minHealthyUptime, // exactly the threshold counts as healthy
			want:   base,
		},
		{
			name:   "long-lived idle reconnect resets to base",
			prev:   16 * time.Second,
			dialed: true,
			uptime: 5 * time.Minute,
			want:   base,
		},
		{
			name:   "sub-threshold flap grows the backoff (x2)",
			prev:   2 * time.Second,
			dialed: true,
			uptime: minHealthyUptime - time.Millisecond, // just under -> flap
			want:   4 * time.Second,
		},
		{
			name:   "instant accept-then-close flap grows from base",
			prev:   base,
			dialed: true,
			uptime: 5 * time.Millisecond,
			want:   2 * time.Second,
		},
		{
			name:   "failed dial grows the backoff (x2)",
			prev:   2 * time.Second,
			dialed: false,
			uptime: 0,
			want:   4 * time.Second,
		},
		{
			name:   "growth is capped at max",
			prev:   20 * time.Second,
			dialed: false,
			uptime: 0,
			want:   max, // 40s -> capped to 30s
		},
		{
			name:   "flap from max stays at max",
			prev:   max,
			dialed: true,
			uptime: time.Millisecond,
			want:   max,
		},
		{
			name:   "zero prev floors to base on flap",
			prev:   0,
			dialed: true,
			uptime: time.Millisecond,
			want:   base,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextBackoff(tt.prev, base, max, tt.dialed, tt.uptime)
			if got != tt.want {
				t.Fatalf("nextBackoff(prev=%s, dialed=%v, uptime=%s) = %s, want %s",
					tt.prev, tt.dialed, tt.uptime, got, tt.want)
			}
		})
	}
}

// jitter must stay within [0, d] (full jitter) so a fleet / clones of one
// identity decorrelate their reconnect sleeps instead of phase-locking, while
// never exceeding the computed backoff ceiling.
func TestJitterWithinBounds(t *testing.T) {
	rt := &Runtime{}
	if got := rt.jitter(0); got != 0 {
		t.Fatalf("jitter(0) = %s, want 0", got)
	}
	if got := rt.jitter(-time.Second); got != 0 {
		t.Fatalf("jitter(negative) = %s, want 0", got)
	}
	const ceiling = 4 * time.Second
	for i := 0; i < 1000; i++ {
		d := rt.jitter(ceiling)
		if d < 0 || d > ceiling {
			t.Fatalf("jitter(%s) = %s, out of [0,%s]", ceiling, d, ceiling)
		}
	}
}

// closeCodeReason must surface a deliberate relay close (code+reason) instead of
// discarding it, and degrade gracefully for non-close errors.
func TestCloseCodeReason(t *testing.T) {
	// nil -> sentinel, empty reason.
	if code, reason := closeCodeReason(nil); code != -1 || reason != "" {
		t.Fatalf("closeCodeReason(nil) = (%d,%q), want (-1,\"\")", code, reason)
	}
	// A websocket close handshake carries code+reason; we must recover both even
	// when wrapped (errors.As walks the chain).
	ce := wrapCloseErr(websocket.CloseError{Code: 4001, Reason: "agent registration proof required"})
	if code, reason := closeCodeReason(ce); code != 4001 || reason != "agent registration proof required" {
		t.Fatalf("closeCodeReason(close) = (%d,%q), want (4001,%q)", code, reason, "agent registration proof required")
	}
	// A plain error -> sentinel code, message preserved as the reason.
	if code, reason := closeCodeReason(errPlain("boom")); code != -1 || reason != "boom" {
		t.Fatalf("closeCodeReason(plain) = (%d,%q), want (-1,\"boom\")", code, reason)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }

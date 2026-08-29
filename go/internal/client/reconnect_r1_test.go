package client

// R1 policy tests: bounded give-up budget, flap accounting, and the prompt
// (no-backoff) resume after a healthy drop. See web/test/reconnect.test.js for
// the mirrored browser-side semantics.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

// testClock is a manually-advanced clock for the policy's now hook.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestReconnectLoopGivesUpAfterBudget(t *testing.T) {
	var sleeps []time.Duration
	var gaveUpFailures int
	dialErr := errors.New("dial: relay down")
	p := ReconnectPolicy{
		Base: time.Second, Cap: 4 * time.Second, MaxFailures: 5,
		Notify: ReconnectNotify{OnGaveUp: func(failures int, lastErr error) {
			gaveUpFailures = failures
			if !errors.Is(lastErr, dialErr) {
				t.Errorf("OnGaveUp lastErr = %v, want the dial error", lastErr)
			}
		}},
		now:   time.Now,
		sleep: func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil },
	}
	err := ReconnectLoopWith(context.Background(), p,
		func(context.Context) (peer.MsgConn, *noise.Session, func(), error) { return nil, nil, nil, dialErr },
		func(context.Context, peer.MsgConn, *noise.Session) error { t.Fatal("must never run"); return nil })
	if !errors.Is(err, ErrReconnectGaveUp) {
		t.Fatalf("err = %v, want ErrReconnectGaveUp", err)
	}
	if gaveUpFailures != 5 {
		t.Fatalf("OnGaveUp failures = %d, want 5", gaveUpFailures)
	}
	// 4 sleeps before the 5th, final failure; doubling from Base, bounded by Cap.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
}

func TestReconnectLoopFlapBurnsBudgetAndGivesUp(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)} // non-zero: zero time is the loop's first-connect sentinel
	var attempts []int
	p := ReconnectPolicy{
		MaxFailures: 3, MinHealthy: 5 * time.Second,
		Notify: ReconnectNotify{OnReconnecting: func(a int) { attempts = append(attempts, a) }},
		now:    clk.now,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	err := ReconnectLoopWith(context.Background(), p,
		func(context.Context) (peer.MsgConn, *noise.Session, func(), error) {
			return fakeConn{tag: "c"}, nil, func() {}, nil
		},
		// connects, then dies young (the clock does not advance): a flap every time.
		func(context.Context, peer.MsgConn, *noise.Session) error { return peer.ErrDataChannelClosed })
	if !errors.Is(err, ErrReconnectGaveUp) {
		t.Fatalf("err = %v, want ErrReconnectGaveUp", err)
	}
	// Flaps 1..3 burn the budget; redials 2 and 3 announced as attempts 1 and 2.
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("OnReconnecting attempts = %v, want [1 2]", attempts)
	}
}

func TestReconnectLoopHealthyDropResumesPromptly(t *testing.T) {
	clk := &testClock{t: time.Unix(1000, 0)} // non-zero: zero time is the loop's first-connect sentinel
	var resumed []time.Duration
	var attempts []int
	runs := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ReconnectPolicy{
		MinHealthy: 5 * time.Second,
		Notify: ReconnectNotify{
			OnReconnecting: func(a int) { attempts = append(attempts, a) },
			OnResumed:      func(d time.Duration) { resumed = append(resumed, d) },
		},
		now: clk.now,
		// A healthy drop must redial with NO backoff sleep at all.
		sleep: func(context.Context, time.Duration) error { t.Fatal("healthy drop must not sleep"); return nil },
	}
	runsDial := 0
	err := ReconnectLoopWith(ctx, p,
		func(context.Context) (peer.MsgConn, *noise.Session, func(), error) {
			runsDial++
			if runsDial == 2 {
				clk.advance(1500 * time.Millisecond) // the outage: the redial takes 1.5s
			}
			return fakeConn{tag: "c"}, nil, func() {}, nil
		},
		func(context.Context, peer.MsgConn, *noise.Session) error {
			runs++
			if runs == 1 {
				clk.advance(10 * time.Second) // healthy uptime, then drop
				return peer.ErrDataChannelClosed
			}
			cancel()
			return nil
		})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(attempts) != 1 || attempts[0] != 1 {
		t.Fatalf("OnReconnecting attempts = %v, want [1]", attempts)
	}
	if len(resumed) != 1 || resumed[0] != 1500*time.Millisecond {
		t.Fatalf("OnResumed = %v, want [1.5s]", resumed)
	}
}

// go/internal/agent/windowpush_test.go
package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// pushRecorder is a fake snapshot source plus sink: every rebuild returns a new
// value (unless frozen) so a push proves a rebuild happened.
type pushRecorder struct {
	mu     sync.Mutex
	builds int
	pushed [][]byte
	freeze bool // return the same snapshot every time
	got    chan struct{}
}

func newPushRecorder() *pushRecorder {
	return &pushRecorder{got: make(chan struct{}, 16)}
}

func (r *pushRecorder) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builds++
	if r.freeze {
		return []byte("same")
	}
	return []byte(fmt.Sprintf("snap-%d", r.builds))
}

func (r *pushRecorder) push(b []byte) error {
	r.mu.Lock()
	r.pushed = append(r.pushed, b)
	r.mu.Unlock()
	select {
	case r.got <- struct{}{}:
	default:
	}
	return nil
}

func (r *pushRecorder) counts() (builds, pushes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builds, len(r.pushed)
}

// drain empties the push notification channel, so a later receive can only mean
// a push that happened after the drain.
func (r *pushRecorder) drain() {
	for {
		select {
		case <-r.got:
		default:
			return
		}
	}
}

// waitPushes waits until at least n snapshots have been pushed.
func (r *pushRecorder) waitPushes(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, p := r.counts(); p >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, p := r.counts()
	t.Fatalf("wanted %d pushes within %v, got %d", n, within, p)
}

// runPusher starts pushWindowSnapshots and returns a stop func that waits for it.
func runPusher(t *testing.T, r *pushRecorder, trigger <-chan struct{}, poll, debounce time.Duration) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pushWindowSnapshots(ctx, r.snapshot, r.push, trigger, poll, debounce)
	}()
	return func() {
		cancel()
		<-done
	}
}

// TestPushWindowSnapshotsPushesFirstSnapshot: a client must get the strip on
// attach, without waiting for an event or a tick.
func TestPushWindowSnapshotsPushesFirstSnapshot(t *testing.T) {
	r := newPushRecorder()
	stop := runPusher(t, r, nil, time.Hour, time.Hour)
	defer stop()
	r.waitPushes(t, 1, time.Second)
}

// TestPushWindowSnapshotsTriggerBeatsThePoll is the point of the slice: an event
// reaches the client far sooner than the poll would carry it. The poll is an
// hour away here, so only the trigger path can produce the second push.
func TestPushWindowSnapshotsTriggerBeatsThePoll(t *testing.T) {
	r := newPushRecorder()
	trigger := make(chan struct{}, 1)
	stop := runPusher(t, r, trigger, time.Hour, 20*time.Millisecond)
	defer stop()
	r.waitPushes(t, 1, time.Second)
	r.drain()

	start := time.Now()
	trigger <- struct{}{}
	select {
	case <-r.got:
	case <-time.After(time.Second):
		t.Fatal("trigger produced no push")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("push took %v after the trigger, want < 200ms", d)
	}
}

// TestPushWindowSnapshotsCoalescesBurst: one user action can fire several tmux
// hooks; they must cost one rebuild, not one each.
func TestPushWindowSnapshotsCoalescesBurst(t *testing.T) {
	r := newPushRecorder()
	trigger := make(chan struct{}, 1)
	stop := runPusher(t, r, trigger, time.Hour, 60*time.Millisecond)
	defer stop()
	r.waitPushes(t, 1, time.Second)
	r.drain()
	buildsBefore, _ := r.counts()

	for i := 0; i < 20; i++ {
		select {
		case trigger <- struct{}{}:
		default: // the pusher has not drained yet; the burst folds in anyway
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-r.got:
	case <-time.After(time.Second):
		t.Fatal("burst produced no push")
	}
	time.Sleep(150 * time.Millisecond) // let any straggler rebuild land

	builds, pushes := r.counts()
	if builds-buildsBefore != 1 {
		t.Errorf("burst caused %d rebuilds, want 1", builds-buildsBefore)
	}
	if pushes != 2 {
		t.Errorf("pushes = %d, want 2 (first snapshot + one for the burst)", pushes)
	}
}

// TestPushWindowSnapshotsSkipsUnchanged: a hook that fires without changing the
// snapshot must not put a frame on the wire.
func TestPushWindowSnapshotsSkipsUnchanged(t *testing.T) {
	r := newPushRecorder()
	r.freeze = true
	trigger := make(chan struct{}, 1)
	stop := runPusher(t, r, trigger, time.Hour, 10*time.Millisecond)
	defer stop()
	r.waitPushes(t, 1, time.Second)

	trigger <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	if _, pushes := r.counts(); pushes != 1 {
		t.Fatalf("pushes = %d, want 1 (the snapshot never changed)", pushes)
	}
	if builds, _ := r.counts(); builds < 2 {
		t.Fatalf("rebuilds = %d, want the trigger to have rebuilt", builds)
	}
}

// TestPushWindowSnapshotsPollsWithoutTrigger: with no event source (tmux too old
// for hooks, or hooks refused) the poll still carries changes.
func TestPushWindowSnapshotsPollsWithoutTrigger(t *testing.T) {
	r := newPushRecorder()
	stop := runPusher(t, r, nil, 10*time.Millisecond, time.Hour)
	defer stop()
	r.waitPushes(t, 3, 2*time.Second)
}

// TestPushWindowSnapshotsSurvivesClosedTrigger: when the hook loop gives up and
// closes its channel, the pusher must fall back to the poll, not spin.
func TestPushWindowSnapshotsSurvivesClosedTrigger(t *testing.T) {
	r := newPushRecorder()
	trigger := make(chan struct{})
	stop := runPusher(t, r, trigger, time.Hour, time.Hour)
	defer stop()
	r.waitPushes(t, 1, time.Second)

	close(trigger)
	time.Sleep(100 * time.Millisecond)
	if builds, _ := r.counts(); builds > 2 {
		t.Fatalf("rebuilds = %d after the trigger closed, want the loop idle", builds)
	}
}

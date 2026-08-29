package peer

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestLinkWatchKillsAfterGrace(t *testing.T) {
	var kills int32
	w := NewLinkWatch(10*time.Millisecond, func() { atomic.AddInt32(&kills, 1) })
	w.State(webrtc.PeerConnectionStateConnected)
	w.State(webrtc.PeerConnectionStateDisconnected)
	w.State(webrtc.PeerConnectionStateDisconnected) // repeats must not re-arm
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&kills) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond) // would catch a second, re-armed fire
	if got := atomic.LoadInt32(&kills); got != 1 {
		t.Fatalf("kills = %d, want exactly 1", got)
	}
}

func TestLinkWatchRecoveryDisarms(t *testing.T) {
	var kills int32
	w := NewLinkWatch(10*time.Millisecond, func() { atomic.AddInt32(&kills, 1) })
	w.State(webrtc.PeerConnectionStateDisconnected)
	w.State(webrtc.PeerConnectionStateConnected) // healed inside the grace
	time.Sleep(40 * time.Millisecond)
	if got := atomic.LoadInt32(&kills); got != 0 {
		t.Fatalf("kills = %d, want 0 after recovery", got)
	}
}

func TestLinkWatchDeadStatesDisarm(t *testing.T) {
	for _, s := range []webrtc.PeerConnectionState{webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed} {
		var kills int32
		w := NewLinkWatch(10*time.Millisecond, func() { atomic.AddInt32(&kills, 1) })
		w.State(webrtc.PeerConnectionStateDisconnected)
		w.State(s) // ICESessionDead's teardown owns this path
		time.Sleep(40 * time.Millisecond)
		if got := atomic.LoadInt32(&kills); got != 0 {
			t.Fatalf("kills after %s = %d, want 0", s, got)
		}
	}
}

func TestLinkWatchStopDisarms(t *testing.T) {
	var kills int32
	w := NewLinkWatch(10*time.Millisecond, func() { atomic.AddInt32(&kills, 1) })
	w.State(webrtc.PeerConnectionStateDisconnected)
	w.Stop()
	time.Sleep(40 * time.Millisecond)
	if got := atomic.LoadInt32(&kills); got != 0 {
		t.Fatalf("kills = %d, want 0 after Stop", got)
	}
}

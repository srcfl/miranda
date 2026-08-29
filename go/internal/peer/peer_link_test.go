package peer

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// TestLinkTimingLeavesRoomForTheResumeGate pins the arithmetic behind the beta's
// reconnect gate (roadmap R1: p90 under 3s from a network flip to bytes flowing
// again). This package owns detection — how long a dead path sits unnoticed —
// and the reconnect loop owns the redial that follows it. netsim measures the
// redial at ~0.8s direct and ~1.2s relayed, so detection has to fit in what is
// left. Retuning either constant is fine; doing it without facing this sum is
// how the gate quietly reopens.
func TestLinkTimingLeavesRoomForTheResumeGate(t *testing.T) {
	const (
		resumeGate      = 3 * time.Second
		measuredRedial  = 1250 * time.Millisecond // netsim flip-turn, the slower path
		detectionBudget = iceDisconnectedTimeout + LinkGrace
	)
	if got := detectionBudget + measuredRedial; got >= resumeGate {
		t.Fatalf("detection %v + measured redial %v = %v, which does not fit the %v resume gate",
			detectionBudget, measuredRedial, got, resumeGate)
	}
	// Detection is deliberately keepalive-driven: waiting for fewer than two
	// missed keepalives would call a single lost packet a dead link.
	if iceDisconnectedTimeout < 2*iceKeepAlive {
		t.Fatalf("iceDisconnectedTimeout %v is under two %v keepalives — one lost packet would tear down a live session",
			iceDisconnectedTimeout, iceKeepAlive)
	}
	if iceFailedTimeout <= iceDisconnectedTimeout {
		t.Fatalf("iceFailedTimeout %v must outlast iceDisconnectedTimeout %v", iceFailedTimeout, iceDisconnectedTimeout)
	}
}

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

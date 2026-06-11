// go/internal/signal/proofstore_test.go
package signal

import (
	"strconv"
	"testing"
	"time"
)

func TestProofStoreTOFUAndMatch(t *testing.T) {
	p := newProofStore(8)
	if !p.ok("o|m", "") {
		t.Fatal("unlearned slot must be open (TOFU)")
	}
	p.learn("o|m", "secret")
	if p.ok("o|m", "wrong") {
		t.Fatal("wrong proof must be rejected once learned")
	}
	if !p.ok("o|m", "secret") {
		t.Fatal("matching proof must be accepted")
	}
	if p.ok("o|m", "") {
		t.Fatal("empty proof must be rejected once a secret is learned")
	}
}

func TestProofStoreLearnIsWriteOnce(t *testing.T) {
	p := newProofStore(8)
	p.learn("o|m", "first")
	p.learn("o|m", "second") // must NOT overwrite (preserves the original semantics)
	if !p.ok("o|m", "first") {
		t.Fatal("first learned proof must stick")
	}
	if p.ok("o|m", "second") {
		t.Fatal("a later learn must not overwrite the first proof")
	}
}

func TestProofStoreRejectsEmptyAndOverlongProofs(t *testing.T) {
	p := newProofStore(8)
	p.learn("a|b", "") // legacy no-secret agent: stays open
	if p.len() != 0 {
		t.Fatalf("empty proof must not be stored, len=%d", p.len())
	}
	huge := make([]byte, maxAgentProofBytes+1)
	for i := range huge {
		huge[i] = 'x'
	}
	p.learn("c|d", string(huge)) // over-long: must not be stored (memory-amplification guard)
	if p.len() != 0 {
		t.Fatalf("over-long proof must not be stored, len=%d", p.len())
	}
	if !p.ok("c|d", "anything") {
		t.Fatal("slot with an unstored over-long proof must remain open")
	}
}

// TestProofStoreBoundsGrowth is the core DoS guard: inserting far more distinct
// slots than the cap must not grow the store past max.
func TestProofStoreBoundsGrowth(t *testing.T) {
	const max = 16
	p := newProofStore(max)
	for i := 0; i < 10*max; i++ {
		p.learn("owner|"+strconv.Itoa(i), "secret-"+strconv.Itoa(i))
		if p.len() > max {
			t.Fatalf("store grew past cap: len=%d max=%d after %d inserts", p.len(), max, i+1)
		}
	}
	if p.len() != max {
		t.Fatalf("store should be saturated at the cap: len=%d max=%d", p.len(), max)
	}
}

// TestProofStoreEvictsLeastRecentlySeen verifies a recently-touched (live) slot
// survives eviction while a cold slot is dropped.
func TestProofStoreEvictsLeastRecentlySeen(t *testing.T) {
	const max = 3
	p := newProofStore(max)
	p.learn("k|live", "live")
	p.learn("k|cold", "cold")
	p.learn("k|warm", "warm")
	// Touch "live" so it is the most-recently-seen; "cold" is now the LRU victim.
	if !p.ok("k|live", "live") {
		t.Fatal("live proof should match")
	}
	p.learn("k|new", "new") // forces one eviction (the LRU = "cold")
	if p.len() != max {
		t.Fatalf("len=%d, want %d", p.len(), max)
	}
	if !p.ok("k|live", "live") {
		t.Fatal("recently-seen live slot must survive eviction")
	}
	if !p.ok("k|cold", "") {
		t.Fatal("least-recently-seen slot should have been evicted (now open again)")
	}
}

// TestFlapCounterFiresAboveThreshold is the core alert logic: with threshold 3,
// the 4th replacement inside the window is the first to report a flap. This is
// the same-identity ping-pong (two agents under one owner|machine each replacing
// the other every ~1s).
func TestFlapCounterFiresAboveThreshold(t *testing.T) {
	f := newFlapCounter(3, 30*time.Second, 8)
	base := time.Unix(1_700_000_000, 0)
	// 1s apart, well inside the 30s window.
	for i := 1; i <= 3; i++ {
		flapped, count := f.record("o|m", base.Add(time.Duration(i)*time.Second))
		if flapped {
			t.Fatalf("replacement %d (count=%d) must not flap at threshold 3", i, count)
		}
		if count != i {
			t.Fatalf("replacement %d: count=%d, want %d", i, count, i)
		}
	}
	flapped, count := f.record("o|m", base.Add(4*time.Second))
	if !flapped {
		t.Fatalf("4th replacement inside window must flap (count=%d)", count)
	}
	if count != 4 {
		t.Fatalf("count=%d, want 4", count)
	}
}

// TestFlapCounterAgesOutOldReplacements verifies the sliding window: replacements
// older than the window do not count, so a slowly-restarting agent never trips
// the alert.
func TestFlapCounterAgesOutOldReplacements(t *testing.T) {
	f := newFlapCounter(3, 30*time.Second, 8)
	base := time.Unix(1_700_000_000, 0)
	// Four replacements spaced 20s apart: any 30s window holds at most 2, so it
	// must never flap.
	for i := 0; i < 4; i++ {
		flapped, count := f.record("o|m", base.Add(time.Duration(i)*20*time.Second))
		if flapped {
			t.Fatalf("spaced replacement %d must not flap (count=%d)", i, count)
		}
		if count > 2 {
			t.Fatalf("window should retain at most 2 stamps, got count=%d at i=%d", count, i)
		}
	}
}

// TestFlapCounterIsPerKey verifies one flapping slot does not implicate a
// different owner|machine.
func TestFlapCounterIsPerKey(t *testing.T) {
	f := newFlapCounter(3, 30*time.Second, 8)
	base := time.Unix(1_700_000_000, 0)
	for i := 1; i <= 4; i++ {
		f.record("hot|m", base.Add(time.Duration(i)*time.Second))
	}
	flapped, count := f.record("calm|m", base.Add(time.Second))
	if flapped {
		t.Fatalf("a quiet key must not inherit another key's flap (count=%d)", count)
	}
	if count != 1 {
		t.Fatalf("quiet key count=%d, want 1", count)
	}
}

// TestFlapCounterBoundsGrowth is the DoS guard mirroring the proof store: a flood
// of distinct slots must not grow the counter past its cap.
func TestFlapCounterBoundsGrowth(t *testing.T) {
	const max = 16
	f := newFlapCounter(3, 30*time.Second, max)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 10*max; i++ {
		f.record("owner|"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second))
		if f.len() > max {
			t.Fatalf("flap counter grew past cap: len=%d max=%d after %d records", f.len(), max, i+1)
		}
	}
	if f.len() != max {
		t.Fatalf("flap counter should saturate at the cap: len=%d max=%d", f.len(), max)
	}
}

// TestFlapCounterEvictsOldest verifies an actively-flapping slot survives
// eviction (it is refreshed on every replacement) while a cold slot is dropped.
func TestFlapCounterEvictsOldest(t *testing.T) {
	const max = 2
	f := newFlapCounter(3, time.Hour, max)
	base := time.Unix(1_700_000_000, 0)
	f.record("k|cold", base)                 // oldest, never touched again
	f.record("k|hot", base.Add(time.Second)) // will be refreshed below
	// Refresh "hot" so "cold" is the oldest-by-last-replacement victim.
	f.record("k|hot", base.Add(2*time.Second))
	f.record("k|new", base.Add(3*time.Second)) // forces one eviction (cold)
	if f.len() != max {
		t.Fatalf("len=%d, want %d", f.len(), max)
	}
	if _, ok := f.slots["k|cold"]; ok {
		t.Fatal("least-recently-replaced slot must be evicted")
	}
	if _, ok := f.slots["k|hot"]; !ok {
		t.Fatal("recently-refreshed slot must survive eviction")
	}
}

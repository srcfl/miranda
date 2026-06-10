// go/internal/signal/proofstore_test.go
package signal

import (
	"strconv"
	"testing"
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

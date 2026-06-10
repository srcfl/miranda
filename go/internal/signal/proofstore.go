// go/internal/signal/proofstore.go
package signal

import "crypto/subtle"

const (
	// defaultMaxAgentProofs bounds how many learned registration proofs the relay
	// retains. Without a bound, the proof map grew forever: anyone can insert an
	// entry by opening /agent/signal with an arbitrary owner_id+machine_id and any
	// non-empty proof header, and entries were never evicted — an unauthenticated
	// memory-growth DoS. 65536 is far above any realistic fleet of real machines.
	defaultMaxAgentProofs = 65536
	// maxAgentProofBytes caps the stored proof length. The real registration secret
	// is 32 bytes rendered as 64 hex chars; this bounds an attacker who sends a huge
	// header so a stored entry can't amplify memory. Over-long proofs are not stored
	// (the slot stays trust-on-first-use), which only ever affects an attacker.
	maxAgentProofBytes = 512
)

// proofStore holds learned agent registration proofs (owner|machine -> secret)
// with a bounded entry count. It is NOT internally synchronized: the Server holds
// s.mu around every call, matching the previous bare-map access. When full, it
// evicts the least-recently-seen slot — a live agent's slot is refreshed on every
// (re)registration, so it is the last thing evicted.
type proofStore struct {
	max     int
	clock   uint64 // logical clock for LRU recency (no wall-clock dependency)
	entries map[string]*proofEntry
}

type proofEntry struct {
	secret string
	seen   uint64
}

func newProofStore(max int) *proofStore {
	if max <= 0 {
		max = defaultMaxAgentProofs
	}
	return &proofStore{max: max, entries: map[string]*proofEntry{}}
}

// ok reports whether proof may (re)register slot k. A slot with no learned proof
// is open (trust-on-first-use); otherwise the proof must match in constant time.
// Touching an existing slot refreshes its LRU recency.
func (p *proofStore) ok(k, proof string) bool {
	e := p.entries[k]
	if e == nil {
		return true
	}
	p.clock++
	e.seen = p.clock
	return subtle.ConstantTimeCompare([]byte(proof), []byte(e.secret)) == 1
}

// learn records proof for slot k the first time the slot presents a non-empty
// proof. It is a no-op for an empty proof (legacy no-secret agent), an over-long
// proof, or a slot that already has a proof — preserving the original
// "set only if currently empty" semantics. A new entry may evict the
// least-recently-seen slot to stay within max.
func (p *proofStore) learn(k, proof string) {
	if proof == "" || len(proof) > maxAgentProofBytes {
		return
	}
	if _, ok := p.entries[k]; ok {
		return
	}
	if len(p.entries) >= p.max {
		p.evictLRU()
	}
	p.clock++
	p.entries[k] = &proofEntry{secret: proof, seen: p.clock}
}

// evictLRU removes the entry with the smallest seen clock. O(n); only runs once
// the store is at capacity, which for real fleets never happens and under attack
// is gated by the documented Cloudflare rate limit on /agent/signal.
func (p *proofStore) evictLRU() {
	var oldestKey string
	var oldest uint64
	first := true
	for k, e := range p.entries {
		if first || e.seen < oldest {
			oldestKey, oldest, first = k, e.seen, false
		}
	}
	if !first {
		delete(p.entries, oldestKey)
	}
}

func (p *proofStore) len() int { return len(p.entries) }

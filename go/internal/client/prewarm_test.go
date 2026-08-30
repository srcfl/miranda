package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// fakeClock drives the TTL logic without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testRelay is a stand-in for mir-signal: it counts hits per path, serves what
// the test puts in it, and can gate any handler so a test can control ordering.
type testRelay struct {
	*httptest.Server
	mu          sync.Mutex
	hits        map[string]int
	revocations []identity.Revocation
	entries     []any
	turnExpiry  time.Time
	noTURN      bool
	gate        func(path string)
}

func newTestRelay(t *testing.T) *testRelay {
	t.Helper()
	r := &testRelay{hits: map[string]int{}}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits[req.URL.Path]++
		gate, revs, entries := r.gate, append([]identity.Revocation(nil), r.revocations...), append([]any(nil), r.entries...)
		expiry, noTURN := r.turnExpiry, r.noTURN
		r.mu.Unlock()
		if gate != nil {
			gate(req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/revocations":
			_ = json.NewEncoder(w).Encode(revs)
		case "/registry":
			_ = json.NewEncoder(w).Encode(entries)
		case "/turn-credentials":
			if noTURN {
				http.Error(w, "turn not configured", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"username": strconv.FormatInt(expiry.Unix(), 10),
				"password": "cred",
				"ttl":      900,
				"urls":     []string{"turn:relay.example:3478"},
			})
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *testRelay) hitCount(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func (r *testRelay) setGate(fn func(path string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate = fn
}

func (r *testRelay) setRevocations(records ...identity.Revocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revocations = records
}

func (r *testRelay) setEntries(entries ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = entries
}

func (r *testRelay) setTURNExpiry(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnExpiry = at
}

// runPrewarm runs a warm-up with a watchdog, so a step that blocks when it must
// not fails the test instead of hanging it.
func runPrewarm(t *testing.T, p *Prewarmer, req PrewarmRequest) *Prewarm {
	t.Helper()
	done := make(chan *Prewarm, 1)
	go func() { done <- p.Run(context.Background(), req) }()
	select {
	case w := <-done:
		return w
	case <-time.After(15 * time.Second):
		t.Fatal("pre-attach warm-up blocked")
		return nil
	}
}

func testSignerRevocation(t *testing.T, secretHex, machineID string) identity.Revocation {
	t.Helper()
	signer, err := identity.DeriveSigner(mustHex(t, secretHex))
	if err != nil {
		t.Fatal(err)
	}
	record, err := signer.SignRevocation(machineID, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return *record
}

// ---------------------------------------------------------------------------
// TTL logic (fake clock, no I/O)
// ---------------------------------------------------------------------------

func TestDirectoryPlanAgesTheCache(t *testing.T) {
	now := newFakeClock().now()
	at := func(d time.Duration) *cacheEnvelope { return &cacheEnvelope{FetchedAt: now.Add(-d)} }
	cases := []struct {
		name string
		env  *cacheEnvelope
		want cachePlan
	}{
		{"no cache", nil, planFetch},
		{"inside the TTL", at(10 * time.Second), planFresh},
		{"just past the TTL", at(DirectoryTTL + time.Second), planStale},
		{"just inside the ceiling", at(DirectoryStaleCeiling - time.Second), planStale},
		{"past the ceiling", at(DirectoryStaleCeiling + time.Second), planFetch},
		{"stamped in the future", at(-time.Hour), planFetch},
	}
	for _, tc := range cases {
		if got := directoryPlan(tc.env, now); got != tc.want {
			t.Errorf("%s: plan = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTURNPlanHonorsTheRenewMargin(t *testing.T) {
	now := newFakeClock().now()
	creds := func(left time.Duration) *cacheEnvelope {
		return &cacheEnvelope{FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(left)}
	}
	cases := []struct {
		name string
		env  *cacheEnvelope
		want cachePlan
	}{
		{"no cache", nil, planFetch},
		{"plenty of life left", creds(14 * time.Minute), planFresh},
		{"inside the renew margin", creds(TURNRenewMargin - time.Second), planStale},
		{"expired", creds(-time.Second), planFetch},
		{"no TURN here, asked recently", &cacheEnvelope{FetchedAt: now.Add(-10 * time.Second)}, planFresh},
		{"no TURN here, asked long ago", &cacheEnvelope{FetchedAt: now.Add(-time.Hour)}, planFetch},
		{"stamped in the future", &cacheEnvelope{FetchedAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour)}, planFetch},
	}
	for _, tc := range cases {
		if got := turnPlan(tc.env, now); got != tc.want {
			t.Errorf("%s: plan = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parallelism
// ---------------------------------------------------------------------------

// The three pre-attach fetches must be in flight together: each handler blocks
// until all three have arrived, so any sequencing deadlocks (and fails) here.
func TestPrewarmRunsTheThreeFetchesInParallel(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	relay := newTestRelay(t)
	relay.setTURNExpiry(newFakeClock().now().Add(15 * time.Minute))

	var mu sync.Mutex
	waiting := 0
	all := make(chan struct{})
	relay.setGate(func(string) {
		mu.Lock()
		waiting++
		if waiting == 3 {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(10 * time.Second):
			t.Error("pre-attach fetches did not overlap")
		}
	})

	clock := newFakeClock()
	p := &Prewarmer{Dir: dir, Now: clock.now}
	warm := runPrewarm(t, p, PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL})
	warm.waitBackground()

	for _, path := range []string{"/revocations", "/registry", "/turn-credentials"} {
		if relay.hitCount(path) != 1 {
			t.Errorf("%s hit %d times, want 1", path, relay.hitCount(path))
		}
	}
	if warm.RegistrySource != SourceRelay || warm.RevocationsSource != SourceRelay || warm.ICESource != SourceRelay {
		t.Fatalf("sources = %q/%q/%q", warm.RegistrySource, warm.RevocationsSource, warm.ICESource)
	}
}

// ---------------------------------------------------------------------------
// cache behavior
// ---------------------------------------------------------------------------

func TestPrewarmWarmCacheMakesNoRequests(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setTURNExpiry(clock.now().Add(15 * time.Minute))
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL}
	cold := runPrewarm(t, p, req)
	cold.waitBackground()
	if len(cold.Discovered) != 1 || cold.Discovered[0].Name != "box" {
		t.Fatalf("cold discovered = %+v", cold.Discovered)
	}

	clock.advance(10 * time.Second)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	for _, path := range []string{"/revocations", "/registry", "/turn-credentials"} {
		if relay.hitCount(path) != 1 {
			t.Errorf("%s hit %d times on the warm run, want no second request", path, relay.hitCount(path))
		}
	}
	if warm.RegistrySource != SourceCacheFresh || warm.RevocationsSource != SourceCacheFresh || warm.ICESource != SourceCacheFresh {
		t.Fatalf("warm sources = %q/%q/%q", warm.RegistrySource, warm.RevocationsSource, warm.ICESource)
	}
	if len(warm.Discovered) != 1 || warm.Discovered[0].Name != "box" {
		t.Fatalf("warm discovered = %+v", warm.Discovered)
	}
	if len(warm.ICE) != 1 || warm.ICE[0].Credential != "cred" {
		t.Fatalf("warm ICE = %+v", warm.ICE)
	}
}

// Past the TTL but inside the ceiling: the cached copy is served at once and the
// relay is asked behind it, so the next run is fresh.
func TestPrewarmServesStaleAndRefreshesBehindIt(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	// The machine list changed, and the relay is slow to say so.
	relay.setEntries(
		sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""),
		sealEntry(t, id.Secret(), "m-2", "loft", "bb22", ""),
	)
	release := make(chan struct{})
	relay.setGate(func(string) { <-release })

	clock.advance(DirectoryTTL + 15*time.Second)
	stale := runPrewarm(t, p, req) // must not wait for the blocked relay
	if stale.RegistrySource != SourceCacheStale {
		t.Fatalf("registry source = %q, want %q", stale.RegistrySource, SourceCacheStale)
	}
	if len(stale.Discovered) != 1 {
		t.Fatalf("stale run should serve the cached list, got %+v", stale.Discovered)
	}
	close(release)
	stale.waitBackground()
	relay.setGate(nil)

	// The refresh landed, so the next run is fresh — and sees the new machine.
	clock.advance(time.Second)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if warm.RegistrySource != SourceCacheFresh || len(warm.Discovered) != 2 {
		t.Fatalf("after the background refresh: source %q, discovered %+v", warm.RegistrySource, warm.Discovered)
	}
}

// The staleness bound: past the ceiling the client waits for the relay, so a
// tombstone published elsewhere is enforced on this attach, not the next one.
func TestPrewarmPastCeilingWaitsForRevocations(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	// Another device retires the machine.
	relay.setRevocations(testSignerRevocation(t, testSecretHex, "m-1"))

	clock.advance(DirectoryStaleCeiling + time.Second)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if warm.RevocationsSource != SourceRelay {
		t.Fatalf("revocations source = %q, want a live fetch past the ceiling", warm.RevocationsSource)
	}
	records, err := ListRevocations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !IsMachineRevoked("m-1", id.OwnerID, records) {
		t.Fatal("a tombstone published past the ceiling was not applied before attach")
	}
	if left := FilterRevoked(warm.Discovered, id.OwnerID, records); len(left) != 0 {
		t.Fatalf("revoked machine survived the list: %+v", left)
	}
}

// Inside the ceiling the revocation fetch does not block — but when it wins the
// race against the other pre-attach work, its tombstones are in the fail-closed
// store before this attach reads it.
func TestPrewarmAppliesRevocationThatWinsTheRace(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	relay.setRevocations(testSignerRevocation(t, testSecretHex, "m-1"))
	// Registry is past the ceiling (it blocks), revocations only stale — and the
	// registry answer is held until the tombstone has landed in the store.
	writeStaleRevocationsCache(t, dir, relay.URL, id.OwnerID, clock.now().Add(-(DirectoryTTL + 5*time.Second)))
	clock.advance(DirectoryStaleCeiling + time.Second)
	relay.setGate(func(path string) {
		if path != "/registry" {
			return
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if records, err := ListRevocations(dir); err == nil && len(records) > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	records, err := ListRevocations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !IsMachineRevoked("m-1", id.OwnerID, records) {
		t.Fatal("a revocation that won the race was not applied before attach")
	}
	if left := FilterRevoked(warm.Discovered, id.OwnerID, records); len(left) != 0 {
		t.Fatalf("revoked machine survived the list: %+v", left)
	}
}

// writeStaleRevocationsCache backdates the revocations cache so a test can put
// that entry in the stale band while another is past the ceiling.
func writeStaleRevocationsCache(t *testing.T, dir, signalURL, ownerID string, at time.Time) {
	t.Helper()
	if err := writeCacheEnvelope(dir, cacheRevocations, cacheScope(signalURL, ownerID), at, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPrewarmCacheIsScopedToRelayAndOwner(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	runPrewarm(t, p, PrewarmRequest{Identity: id, SignalURL: relay.URL}).waitBackground()

	// A rotated identity must not read the previous owner's cached answers.
	other := walletIdentity(t, strings.Repeat("ab", 32))
	clock.advance(time.Second)
	warm := runPrewarm(t, p, PrewarmRequest{Identity: other, SignalURL: relay.URL})
	warm.waitBackground()
	if warm.RegistrySource != SourceRelay {
		t.Fatalf("registry source = %q, want a miss for another owner", warm.RegistrySource)
	}
	if relay.hitCount("/registry") != 2 {
		t.Fatalf("/registry hit %d times, want a second fetch for the new owner", relay.hitCount("/registry"))
	}
}

// ---------------------------------------------------------------------------
// fail soft
// ---------------------------------------------------------------------------

func TestPrewarmFallsBackToTheCacheWhenTheRelayIsDown(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	relay.Close()
	clock.advance(DirectoryStaleCeiling + time.Second)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if warm.RegistryErr == nil {
		t.Fatal("an unreachable relay must be reported, so the caller can say discovery is paused")
	}
	if warm.RegistrySource != SourceCacheFallback || len(warm.Discovered) != 1 {
		t.Fatalf("source %q, discovered %+v — want the saved machines", warm.RegistrySource, warm.Discovered)
	}
}

func TestPrewarmWithNoCacheAndNoRelayReportsTheFailure(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	url := relay.URL
	relay.Close()

	p := &Prewarmer{Dir: dir, Now: clock.now}
	warm := runPrewarm(t, p, PrewarmRequest{Identity: id, SignalURL: url})
	warm.waitBackground()
	if warm.RegistryErr == nil || warm.RegistrySource != SourceError || len(warm.Discovered) != 0 {
		t.Fatalf("err %v, source %q, discovered %+v", warm.RegistryErr, warm.RegistrySource, warm.Discovered)
	}
	if warm.RevocationsErr == nil {
		t.Fatal("a failed revocation fetch must be reported")
	}
}

// A legacy identity has no registry key and no signed tombstones: the warm-up
// touches the relay for neither, exactly as the sequential path did.
func TestPrewarmSkipsDirectoryForLegacyIdentity(t *testing.T) {
	dir := t.TempDir()
	relay := newTestRelay(t)
	clock := newFakeClock()
	p := &Prewarmer{Dir: dir, Now: clock.now}
	warm := runPrewarm(t, p, PrewarmRequest{Identity: &Identity{}, SignalURL: relay.URL})
	warm.waitBackground()
	if relay.hitCount("/registry")+relay.hitCount("/revocations") != 0 {
		t.Fatal("legacy identity must not query the relay directory")
	}
	if warm.RegistrySource != SourceSkipped || warm.RevocationsSource != SourceSkipped {
		t.Fatalf("sources = %q/%q", warm.RegistrySource, warm.RevocationsSource)
	}
}

// ---------------------------------------------------------------------------
// TURN credentials
// ---------------------------------------------------------------------------

func TestPrewarmRefetchesTURNInsideTheRenewMargin(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setTURNExpiry(clock.now().Add(15 * time.Minute))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	// Twelve minutes on, three minutes of life left: still good.
	clock.advance(12 * time.Minute)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if relay.hitCount("/turn-credentials") != 1 || warm.ICESource != SourceCacheFresh {
		t.Fatalf("hits = %d, source = %q — credentials with life left must be reused",
			relay.hitCount("/turn-credentials"), warm.ICESource)
	}

	// Inside the two-minute margin: mint a new one rather than start a session
	// on a credential about to die.
	relay.setTURNExpiry(clock.now().Add(15 * time.Minute))
	clock.advance(90 * time.Second)
	warm = runPrewarm(t, p, req)
	warm.waitBackground()
	if relay.hitCount("/turn-credentials") != 2 || warm.ICESource != SourceRelay {
		t.Fatalf("hits = %d, source = %q — want a refetch inside the renew margin",
			relay.hitCount("/turn-credentials"), warm.ICESource)
	}
}

func TestPrewarmUsesLiveCachedTURNWhenTheRelayIsDown(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setTURNExpiry(clock.now().Add(15 * time.Minute))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL, STUNFallback: []string{defaultSTUN}}
	runPrewarm(t, p, req).waitBackground()

	relay.Close()
	clock.advance(14 * time.Minute) // inside the margin, still valid
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if warm.ICESource != SourceCacheFallback {
		t.Fatalf("ICE source = %q, want the still-valid cached credential", warm.ICESource)
	}
	if len(warm.ICE) != 1 || warm.ICE[0].Credential != "cred" {
		t.Fatalf("ICE = %+v", warm.ICE)
	}
}

// A relay without TURN answers 404. That is a fact about the relay, so it is
// cached too — a STUN-only deployment gets a warm path as well.
func TestPrewarmCachesTheNoTURNAnswer(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.mu.Lock()
	relay.noTURN = true
	relay.mu.Unlock()

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL, STUNFallback: []string{defaultSTUN}}
	cold := runPrewarm(t, p, req)
	cold.waitBackground()
	if len(cold.ICE) != 1 || len(cold.ICE[0].URLs) != 1 || cold.ICE[0].URLs[0] != defaultSTUN {
		t.Fatalf("ICE = %+v, want the STUN fallback", cold.ICE)
	}

	clock.advance(10 * time.Second)
	warm := runPrewarm(t, p, req)
	warm.waitBackground()
	if relay.hitCount("/turn-credentials") != 1 || warm.ICESource != SourceCacheFresh {
		t.Fatalf("hits = %d, source = %q", relay.hitCount("/turn-credentials"), warm.ICESource)
	}
	if len(warm.ICE) != 1 || warm.ICE[0].URLs[0] != defaultSTUN {
		t.Fatalf("warm ICE = %+v", warm.ICE)
	}
}

func TestPrewarmSkipsICEWhenNoneIsAsked(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	relay := newTestRelay(t)
	p := &Prewarmer{Dir: dir, Now: newFakeClock().now}
	warm := runPrewarm(t, p, PrewarmRequest{Identity: id, SignalURL: relay.URL})
	warm.waitBackground()
	if relay.hitCount("/turn-credentials") != 0 || warm.ICESource != SourceSkipped {
		t.Fatalf("hits = %d, source = %q", relay.hitCount("/turn-credentials"), warm.ICESource)
	}
}

// ---------------------------------------------------------------------------
// forced refresh + debug timing
// ---------------------------------------------------------------------------

// A name that resolved nowhere gets one live ask, so a machine paired seconds
// ago on another device is still found while the cache is fresh.
func TestRefreshRegistryBypassesTheCache(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	clock := newFakeClock()
	relay := newTestRelay(t)
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))

	p := &Prewarmer{Dir: dir, Now: clock.now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL}
	runPrewarm(t, p, req).waitBackground()

	relay.setEntries(
		sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""),
		sealEntry(t, id.Secret(), "m-2", "loft", "bb22", ""),
	)
	clock.advance(5 * time.Second)
	warm := runPrewarm(t, p, req)
	if !warm.RegistryIsCached() || len(warm.Discovered) != 1 {
		t.Fatalf("expected the fresh cache first: source %q, %+v", warm.RegistrySource, warm.Discovered)
	}
	live, err := warm.RefreshRegistry(context.Background())
	warm.waitBackground()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || warm.RegistrySource != SourceRelay {
		t.Fatalf("forced refresh = %+v (source %q)", live, warm.RegistrySource)
	}
}

func TestTimingLinesAreDebugOnly(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	relay := newTestRelay(t)
	relay.setTURNExpiry(newFakeClock().now().Add(15 * time.Minute))
	p := &Prewarmer{Dir: dir, Now: newFakeClock().now}
	req := PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL}

	var quiet bytes.Buffer
	swapTimingSink(t, &quiet)
	runPrewarm(t, p, req).waitBackground()
	if quiet.Len() != 0 {
		t.Fatalf("timing lines leaked without MIR_TIMING_DEBUG: %q", quiet.String())
	}

	var loud bytes.Buffer
	swapTimingSink(t, &loud)
	t.Setenv("MIR_TIMING_DEBUG", "1")
	runPrewarm(t, p, req).waitBackground()
	for _, want := range []string{"[timing] revocations", "[timing] registry", "[timing] turn", "[timing] pre-attach total"} {
		if !strings.Contains(loud.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, loud.String())
		}
	}
}

func swapTimingSink(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	timingMu.Lock()
	prev := timingSink
	timingSink = w
	timingMu.Unlock()
	t.Cleanup(func() {
		timingMu.Lock()
		timingSink = prev
		timingMu.Unlock()
	})
}

// The cache must never hold a secret in the clear beyond what the client dir
// already keeps, and never be world-readable.
func TestCacheFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	id := walletIdentity(t, testSecretHex)
	relay := newTestRelay(t)
	relay.setTURNExpiry(newFakeClock().now().Add(15 * time.Minute))
	relay.setEntries(sealEntry(t, id.Secret(), "m-1", "box", "aa11", ""))
	p := &Prewarmer{Dir: dir, Now: newFakeClock().now}
	runPrewarm(t, p, PrewarmRequest{Identity: id, SignalURL: relay.URL, TURNURL: relay.URL}).waitBackground()

	for _, name := range []string{cacheRevocations, cacheRegistry, cacheTURN} {
		info, err := os.Stat(cacheFilePath(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
	// The registry cache holds the relay's sealed wire shape, not plaintext.
	data, err := os.ReadFile(cacheFilePath(dir, cacheRegistry))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("box")) || bytes.Contains(data, []byte("aa11")) {
		t.Fatalf("registry cache leaked opened record fields: %s", data)
	}
}

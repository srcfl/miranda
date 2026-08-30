// go/internal/client/prewarm.go — the warm pre-attach path (spec D2, step 1).
//
// A cold attach used to run three HTTPS round trips back to back: signed
// revocations, then the encrypted registry, then TURN credentials. They do not
// depend on each other, so they now run at once and each answer is cached under
// <dir>/cache. The blocking wait becomes max(three) instead of the sum, and a
// rerun inside the TTL blocks on nothing at all.
//
// Caching never weakens a trust decision. Tombstones fetched here still land in
// the fail-closed local store before the cache timestamp moves, the registry
// cache holds the relay's own blind wire shape (sealed blobs, useless without
// the owner root), and the revocation snapshot an attach is decided on is never
// older than DirectoryStaleCeiling while the relay answers.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/srcful/terminal-relay/go/internal/peer"
)

const (
	// DirectoryTTL is how long a fetched revocations/registry answer counts as
	// fresh. Inside it a rerun makes no request at all.
	DirectoryTTL = 30 * time.Second

	// DirectoryStaleCeiling bounds stale-while-revalidate. Between DirectoryTTL
	// and the ceiling a rerun serves the cached answer and refreshes behind it;
	// past the ceiling it waits for the relay. That ceiling is the honest
	// staleness bound: while the relay answers, no attach is decided on a
	// revocation set older than this.
	DirectoryStaleCeiling = 2 * DirectoryTTL

	// TURNRenewMargin refetches TURN credentials this long before they expire,
	// so a session never starts on a credential about to die (the relay mints
	// them for 15 minutes; see internal/signal/turn.go).
	TURNRenewMargin = 2 * time.Minute

	// prewarmTimeout is the per-fetch budget — unchanged from the sequential
	// path, only no longer paid three times in a row.
	prewarmTimeout = 8 * time.Second
)

// Cache file names under <dir>/cache.
const (
	cacheRevocations = "revocations"
	cacheRegistry    = "registry"
	cacheTURN        = "turn"
)

// Where each answer came from. Surfaced in the debug timing lines (and used by
// callers to tell a cached registry from a live one).
const (
	SourceRelay         = "relay"
	SourceCacheFresh    = "cache-fresh"
	SourceCacheStale    = "cache-stale"
	SourceCacheFallback = "cache-fallback"
	SourceError         = "error"
	SourceSkipped       = "skipped"
)

const cacheVersion = 1

// cacheEnvelope wraps one cached relay answer. Scope pins it to the relay and
// owner it came from, so a rotated identity or a different relay reads as a miss
// instead of a wrong hit.
type cacheEnvelope struct {
	V         int             `json:"v"`
	Scope     string          `json:"scope"`
	FetchedAt time.Time       `json:"fetched_at"`
	ExpiresAt time.Time       `json:"expires_at,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func cacheScope(signalURL, ownerID string) string {
	return strings.TrimRight(strings.TrimSpace(signalURL), "/") + "|" + ownerID
}

func cacheFilePath(dir, name string) string {
	return filepath.Join(dir, "cache", name+".json")
}

// readCacheEnvelope returns the cached answer, or nil for anything that must be
// treated as a miss: absent, unreadable, wrong version, or another scope.
func readCacheEnvelope(dir, name, scope string) *cacheEnvelope {
	data, err := os.ReadFile(cacheFilePath(dir, name))
	if err != nil {
		return nil
	}
	var env cacheEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil
	}
	if env.V != cacheVersion || env.Scope != scope {
		return nil
	}
	return &env
}

// writeCacheEnvelope stores one answer atomically, 0600 inside a 0700 cache dir.
// Every failure is the caller's to ignore: a cache that cannot be written costs
// a round trip, never correctness.
func writeCacheEnvelope(dir, name, scope string, at, expires time.Time, payload any) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cacheEnvelope{
		V: cacheVersion, Scope: scope, FetchedAt: at, ExpiresAt: expires, Payload: blob,
	})
	if err != nil {
		return err
	}
	cdir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cdir, name+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, cacheFilePath(dir, name))
}

// cachePlan says what a cached answer is still good for.
type cachePlan int

const (
	planFetch cachePlan = iota // nothing usable: wait for the relay
	planStale                  // usable but past the TTL: serve it, refresh behind it
	planFresh                  // inside the TTL: serve it, ask nobody
)

// directoryPlan ages a revocations/registry entry. A timestamp in the future is
// a miss, not a licence: a skewed clock (or an edited cache file) must never buy
// unbounded freshness.
func directoryPlan(env *cacheEnvelope, now time.Time) cachePlan {
	if env == nil || now.Before(env.FetchedAt) {
		return planFetch
	}
	switch age := now.Sub(env.FetchedAt); {
	case age < DirectoryTTL:
		return planFresh
	case age < DirectoryStaleCeiling:
		return planStale
	default:
		return planFetch
	}
}

// turnPlan ages cached TURN credentials: fresh while more than the renew margin
// of life is left, still usable (planStale) until the moment they expire, so a
// relay that goes down inside the margin does not cost us the relayed path.
// A "this relay offers no TURN" answer carries no expiry and ages like a
// directory entry.
func turnPlan(env *cacheEnvelope, now time.Time) cachePlan {
	if env == nil || now.Before(env.FetchedAt) {
		return planFetch
	}
	if env.ExpiresAt.IsZero() {
		if now.Sub(env.FetchedAt) < DirectoryTTL {
			return planFresh
		}
		return planFetch
	}
	switch {
	case now.Before(env.ExpiresAt.Add(-TURNRenewMargin)):
		return planFresh
	case now.Before(env.ExpiresAt):
		return planStale
	default:
		return planFetch
	}
}

// Prewarmer runs the pre-attach fetches. Dir is the client state directory; HTTP
// and Now are test seams (nil takes the production defaults).
type Prewarmer struct {
	Dir  string
	HTTP *http.Client
	Now  func() time.Time
}

func (p *Prewarmer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// PrewarmRequest is one warm-up. TURNURL empty skips the ICE fetch entirely
// (commands that never dial a peer do not need credentials).
type PrewarmRequest struct {
	Identity     *Identity
	SignalURL    string
	TURNURL      string
	STUNFallback []string
}

type registryOutcome struct {
	machines []Machine
	err      error
	took     time.Duration
}

type revocationOutcome struct {
	err      error
	storeErr error
	took     time.Duration
}

type iceOutcome struct {
	servers []peer.ICEServer
	err     error
	took    time.Duration
}

// Prewarm is one warm-up's outcome. Discovered and ICE are what the caller
// should use; the *Err fields say what the relay could not do, so callers keep
// today's fail-soft behavior and copy.
type Prewarm struct {
	Discovered     []Machine
	RegistrySource string
	RegistryErr    error

	ICE       []peer.ICEServer
	ICESource string
	// ICEFrom is the relay asked for credentials; "" when none was requested.
	// A caller that ends up attaching through a different relay must not reuse
	// these credentials.
	ICEFrom string
	ICEErr  error

	RevocationsSource string
	RevocationsErr    error
	// RevocationStoreErr is a failure to persist a verified tombstone — the one
	// revocation failure worth a word to the user.
	RevocationStoreErr error

	// Total is the blocking pre-attach wait: max of the three, not their sum.
	Total time.Duration

	pw      *Prewarmer
	req     PrewarmRequest
	regCh   chan registryOutcome
	revCh   chan revocationOutcome
	pending sync.WaitGroup
}

// Run fetches revocations, registry and TURN credentials concurrently, each
// behind its cache, and returns once the answers an attach needs are in hand.
func (p *Prewarmer) Run(ctx context.Context, req PrewarmRequest) *Prewarm {
	start := p.now()
	w := &Prewarm{
		pw: p, req: req,
		RegistrySource:    SourceSkipped,
		RevocationsSource: SourceSkipped,
		ICESource:         SourceSkipped,
	}
	rooted := req.Identity != nil && req.Identity.HasRootedIdentity()

	// ---- plan: local reads only, no network ----------------------------
	revPlan, regPlan := planFresh, planFresh
	var (
		cachedRegistry   []Machine
		cachedRegistryOK bool
		cachedICE        []peer.ICEServer
		cachedICEEnv     *cacheEnvelope
		icePlan          = planFresh
	)
	if rooted {
		scope := cacheScope(req.SignalURL, req.Identity.OwnerID)
		revPlan = directoryPlan(readCacheEnvelope(p.Dir, cacheRevocations, scope), start)
		regEnv := readCacheEnvelope(p.Dir, cacheRegistry, scope)
		regPlan = directoryPlan(regEnv, start)
		if regEnv != nil {
			if machines, err := decodeCachedRegistry(regEnv, req.Identity, req.SignalURL); err == nil {
				cachedRegistry, cachedRegistryOK = machines, true
			} else {
				regPlan = planFetch // unreadable payload: treat it as a miss
			}
		}
	}
	if strings.TrimSpace(req.TURNURL) != "" {
		w.ICEFrom = req.TURNURL
		cachedICEEnv = readCacheEnvelope(p.Dir, cacheTURN, cacheScope(req.TURNURL, ""))
		if cachedICEEnv != nil {
			if err := json.Unmarshal(cachedICEEnv.Payload, &cachedICE); err != nil {
				cachedICEEnv = nil
			}
		}
		icePlan = turnPlan(cachedICEEnv, start)
	}

	// ---- fire: everything that needs the relay starts at once ----------
	if rooted && revPlan != planFresh {
		w.revCh = make(chan revocationOutcome, 1)
		w.pending.Add(1)
		go p.fetchRevocations(fetchCtx(ctx, revPlan), req, w.revCh, &w.pending)
	}
	if rooted && regPlan != planFresh {
		w.regCh = make(chan registryOutcome, 1)
		w.pending.Add(1)
		go p.fetchRegistry(fetchCtx(ctx, regPlan), req, w.regCh, &w.pending)
	}
	var iceCh chan iceOutcome
	if w.ICEFrom != "" && icePlan != planFresh {
		iceCh = make(chan iceOutcome, 1)
		w.pending.Add(1)
		go p.fetchICE(ctx, req, iceCh, &w.pending)
	}

	// ---- join: the blocking steps first, so a racing refresh can land ---
	var regTook, revTook, iceTook time.Duration
	if rooted {
		switch regPlan {
		case planFresh:
			w.Discovered, w.RegistrySource = cachedRegistry, SourceCacheFresh
		case planStale:
			// Serve the cached copy now; take the refresh only if it already won.
			w.Discovered, w.RegistrySource = cachedRegistry, SourceCacheStale
			select {
			case r := <-w.regCh:
				w.regCh = nil
				regTook = r.took
				if r.err != nil {
					w.RegistryErr = r.err
				} else {
					w.Discovered, w.RegistrySource = r.machines, SourceRelay
				}
			default:
			}
		case planFetch:
			r := <-w.regCh
			w.regCh = nil
			regTook = r.took
			switch {
			case r.err == nil:
				w.Discovered, w.RegistrySource = r.machines, SourceRelay
			case cachedRegistryOK:
				// Relay unreachable, cache present: proceed on the cache.
				w.RegistryErr = r.err
				w.Discovered, w.RegistrySource = cachedRegistry, SourceCacheFallback
			default:
				w.RegistryErr, w.RegistrySource = r.err, SourceError
			}
		}
	}

	if w.ICEFrom != "" {
		if icePlan == planFresh {
			w.ICE, w.ICESource = iceServers(cachedICE, req.STUNFallback), SourceCacheFresh
		} else {
			r := <-iceCh
			iceTook = r.took
			switch {
			case r.err == nil:
				w.ICE, w.ICESource = r.servers, SourceRelay
			case len(cachedICE) > 0 && cachedICEEnv != nil && p.now().Before(cachedICEEnv.ExpiresAt):
				// Credentials with life left beat no credentials at all.
				w.ICEErr = r.err
				w.ICE, w.ICESource = iceServers(cachedICE, req.STUNFallback), SourceCacheFallback
			default:
				w.ICEErr, w.ICESource = r.err, SourceError
			}
		}
	}

	if rooted {
		switch revPlan {
		case planFresh:
			w.RevocationsSource = SourceCacheFresh
		case planStale:
			w.RevocationsSource = SourceCacheStale
			// The registry and ICE joins above may have taken a while. If the
			// revocation refresh won that race it is already in the fail-closed
			// store, so this attach is decided on the newer set.
			select {
			case r := <-w.revCh:
				w.revCh = nil
				revTook = r.took
				w.RevocationsErr, w.RevocationStoreErr = r.err, r.storeErr
				if r.err == nil && r.storeErr == nil {
					w.RevocationsSource = SourceRelay
				}
			default:
			}
		case planFetch:
			r := <-w.revCh
			w.revCh = nil
			revTook = r.took
			w.RevocationsErr, w.RevocationStoreErr = r.err, r.storeErr
			if r.err == nil && r.storeErr == nil {
				w.RevocationsSource = SourceRelay
			} else {
				// The local store still enforces every tombstone it already has.
				w.RevocationsSource = SourceError
			}
		}
	}

	w.Total = p.now().Sub(start)
	logTiming("revocations", revTook, w.RevocationsSource)
	logTiming("registry", regTook, w.RegistrySource)
	logTiming("turn", iceTook, w.ICESource)
	logTiming("pre-attach total", w.Total, "")
	return w
}

// RegistryIsCached reports whether Discovered came from disk rather than the
// relay — the caller's cue that a name resolving nowhere deserves one live ask.
func (w *Prewarm) RegistryIsCached() bool {
	return w.RegistrySource == SourceCacheFresh ||
		w.RegistrySource == SourceCacheStale ||
		w.RegistrySource == SourceCacheFallback
}

// RefreshRegistry asks the relay regardless of the cache, for the one path where
// a cached answer is not good enough: a name that resolved nowhere, which a
// machine paired seconds ago on another device would explain. An in-flight
// refresh counts — it is waited for rather than duplicated.
func (w *Prewarm) RefreshRegistry(ctx context.Context) ([]Machine, error) {
	if w == nil || w.pw == nil || w.req.Identity == nil || !w.req.Identity.HasRootedIdentity() {
		return nil, nil
	}
	ch := w.regCh
	if ch == nil {
		ch = make(chan registryOutcome, 1)
		w.pending.Add(1)
		go w.pw.fetchRegistry(ctx, w.req, ch, &w.pending)
	}
	r := <-ch
	w.regCh = nil
	if r.err != nil {
		return nil, r.err
	}
	w.Discovered, w.RegistrySource, w.RegistryErr = r.machines, SourceRelay, nil
	return r.machines, nil
}

// waitBackground blocks until every fetch this warm-up started has finished. It
// exists for tests; production never needs to wait on a background refresh.
func (w *Prewarm) waitBackground() { w.pending.Wait() }

// fetchCtx detaches a stale-while-revalidate refresh from the caller's context:
// it must outlive the command that served the cached copy. Its own timeout still
// bounds it.
func fetchCtx(ctx context.Context, plan cachePlan) context.Context {
	if plan == planStale {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

// fetchRevocations gossips signed tombstones into the fail-closed local store,
// then — and only then — moves the cache timestamp. A crash between the two
// costs one extra fetch, never a missed revocation.
func (p *Prewarmer) fetchRevocations(ctx context.Context, req PrewarmRequest, out chan<- revocationOutcome, wg *sync.WaitGroup) {
	defer wg.Done()
	start := p.now()
	ctx, cancel := context.WithTimeout(ctx, prewarmTimeout)
	defer cancel()
	records, err := FetchRevocations(ctx, p.HTTP, req.SignalURL, req.Identity.OwnerID)
	if err != nil {
		out <- revocationOutcome{err: err, took: p.now().Sub(start)}
		return
	}
	for _, record := range records {
		if err := RecordRevocation(p.Dir, record); err != nil {
			out <- revocationOutcome{storeErr: err, took: p.now().Sub(start)}
			return
		}
	}
	// The payload is deliberately empty: the tombstones live in the verified
	// store, so an edited cache file can at worst cost a fetch or delay one by
	// the ceiling — it can never inject or resurrect a record.
	_ = writeCacheEnvelope(p.Dir, cacheRevocations, cacheScope(req.SignalURL, req.Identity.OwnerID), p.now(), time.Time{}, nil)
	out <- revocationOutcome{took: p.now().Sub(start)}
}

func (p *Prewarmer) fetchRegistry(ctx context.Context, req PrewarmRequest, out chan<- registryOutcome, wg *sync.WaitGroup) {
	defer wg.Done()
	start := p.now()
	ctx, cancel := context.WithTimeout(ctx, prewarmTimeout)
	defer cancel()
	entries, err := fetchRegistryEntries(ctx, p.HTTP, req.SignalURL, req.Identity.OwnerID)
	if err != nil {
		out <- registryOutcome{err: err, took: p.now().Sub(start)}
		return
	}
	machines, err := decodeRegistryEntries(entries, req.Identity, req.SignalURL)
	if err != nil {
		out <- registryOutcome{err: err, took: p.now().Sub(start)}
		return
	}
	// Cached as fetched: sealed blobs, exactly the blind shape the relay serves.
	_ = writeCacheEnvelope(p.Dir, cacheRegistry, cacheScope(req.SignalURL, req.Identity.OwnerID), p.now(), time.Time{}, entries)
	out <- registryOutcome{machines: machines, took: p.now().Sub(start)}
}

func (p *Prewarmer) fetchICE(ctx context.Context, req PrewarmRequest, out chan<- iceOutcome, wg *sync.WaitGroup) {
	defer wg.Done()
	start := p.now()
	ctx, cancel := context.WithTimeout(ctx, prewarmTimeout)
	defer cancel()
	creds, err := peer.FetchTURNCreds(ctx, p.HTTP, req.TURNURL)
	if err != nil {
		out <- iceOutcome{err: err, took: p.now().Sub(start)}
		return
	}
	// A relay without TURN is cached too (empty servers, no expiry): "no TURN
	// here" is a fact about the relay, and re-asking it every attach is a round
	// trip for nothing.
	_ = writeCacheEnvelope(p.Dir, cacheTURN, cacheScope(req.TURNURL, ""), p.now(), creds.Expiry, creds.Servers)
	out <- iceOutcome{servers: iceServers(creds.Servers, req.STUNFallback), took: p.now().Sub(start)}
}

// iceServers mirrors ResolveICE: minted TURN wins (it already yields a
// server-reflexive candidate), otherwise the STUN fallback, otherwise host
// candidates only.
func iceServers(turn []peer.ICEServer, stunFallback []string) []peer.ICEServer {
	if len(turn) > 0 {
		return turn
	}
	if len(stunFallback) == 0 {
		return nil
	}
	return []peer.ICEServer{{URLs: append([]string(nil), stunFallback...)}}
}

// decodeCachedRegistry opens a cached registry answer with the owner root. It
// fails exactly where a live answer would: without the root there is nothing to
// read.
func decodeCachedRegistry(env *cacheEnvelope, id *Identity, signalURL string) ([]Machine, error) {
	var entries []registryEntry
	if err := json.Unmarshal(env.Payload, &entries); err != nil {
		return nil, err
	}
	return decodeRegistryEntries(entries, id, signalURL)
}

// timingSink is where debug timing lines go; a test seam, production is stderr.
var (
	timingSink io.Writer = os.Stderr
	timingMu   sync.Mutex
)

// logTiming prints one debug-level timing line when MIR_TIMING_DEBUG is set,
// matching the [ice] debug lines in internal/peer. netsim reads these to compare
// a cold pre-attach against a warm one.
func logTiming(step string, d time.Duration, source string) {
	if os.Getenv("MIR_TIMING_DEBUG") == "" {
		return
	}
	timingMu.Lock()
	defer timingMu.Unlock()
	if source == "" {
		fmt.Fprintf(timingSink, "[timing] %s %dms\n", step, d.Milliseconds())
		return
	}
	fmt.Fprintf(timingSink, "[timing] %s %dms %s\n", step, d.Milliseconds(), source)
}

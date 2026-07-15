package signal

import (
	"net/http"
	"sync"
	"time"
)

// ratePolicy is a fixed-window admission policy for the initial HTTP/WebSocket
// request. WebSocket frames are bounded separately after upgrade.
type ratePolicy struct {
	Limit  int
	Window time.Duration
}

var defaultRatePolicies = map[string]ratePolicy{
	"/turn-credentials": {Limit: 30, Window: time.Minute},
	"/pair":             {Limit: 20, Window: time.Minute},
	"/attach":           {Limit: 120, Window: time.Minute},
	"/agent/signal":     {Limit: 60, Window: time.Minute},
	"/registry":         {Limit: 120, Window: time.Minute},
	"/revocations":      {Limit: 30, Window: time.Minute},
}

type rateEntry struct {
	count int
	start time.Time
	seen  uint64
}

// requestLimiter is deliberately small and dependency-free. Entries are keyed
// by endpoint + source IP and bounded with LRU eviction so an address flood
// cannot turn the limiter itself into an unbounded memory sink.
type requestLimiter struct {
	mu       sync.Mutex
	entries  map[string]*rateEntry
	policies map[string]ratePolicy
	max      int
	clock    uint64
	now      func() time.Time
}

func newRequestLimiter(max int) *requestLimiter {
	policies := make(map[string]ratePolicy, len(defaultRatePolicies))
	for path, policy := range defaultRatePolicies {
		policies[path] = policy
	}
	return &requestLimiter{entries: make(map[string]*rateEntry), policies: policies, max: max, now: time.Now}
}

func (l *requestLimiter) allow(path, ip string) bool {
	if l == nil {
		return true
	}
	policy, ok := l.policies[path]
	if !ok || policy.Limit <= 0 || policy.Window <= 0 {
		return true
	}
	now := l.now()
	key := path + "|" + ip
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock++
	entry := l.entries[key]
	if entry == nil {
		if l.max > 0 && len(l.entries) >= l.max {
			l.evictLRU()
		}
		l.entries[key] = &rateEntry{count: 1, start: now, seen: l.clock}
		return true
	}
	entry.seen = l.clock
	if now.Sub(entry.start) >= policy.Window {
		entry.count = 1
		entry.start = now
		return true
	}
	if entry.count >= policy.Limit {
		return false
	}
	entry.count++
	return true
}

func (l *requestLimiter) evictLRU() {
	var oldestKey string
	var oldest uint64
	for key, entry := range l.entries {
		if oldestKey == "" || entry.seen < oldest {
			oldestKey, oldest = key, entry.seen
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func (s *Server) rateLimited(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := s.remoteIP(r)
		if !s.limiter.allow(path, ip) {
			w.Header().Set("Retry-After", "60")
			s.logf("event=rate_limit path=%s ip=%s", path, ip)
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

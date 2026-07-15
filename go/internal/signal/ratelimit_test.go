package signal

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestLimiterBoundsAndResets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRequestLimiter(2)
	l.now = func() time.Time { return now }
	l.policies = map[string]ratePolicy{"/x": {Limit: 2, Window: time.Minute}}
	if !l.allow("/x", "a") || !l.allow("/x", "a") {
		t.Fatal("requests within limit were rejected")
	}
	if l.allow("/x", "a") {
		t.Fatal("request above limit was accepted")
	}
	now = now.Add(time.Minute)
	if !l.allow("/x", "a") {
		t.Fatal("window did not reset")
	}
	_ = l.allow("/x", "b")
	_ = l.allow("/x", "c")
	if len(l.entries) != 2 {
		t.Fatalf("entries=%d, want bounded at 2", len(l.entries))
	}
}

func TestRateLimitReturns429BeforeHandler(t *testing.T) {
	s := New()
	s.limiter.policies = map[string]ratePolicy{"/registry": {Limit: 1, Window: time.Minute}}
	h := s.Handler()
	for i, want := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/registry", nil)
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("request %d status=%d want=%d", i+1, rr.Code, want)
		}
		if want == http.StatusTooManyRequests && rr.Header().Get("Retry-After") == "" {
			t.Fatal("429 response missing Retry-After")
		}
	}
}

func TestProxyHeadersRequireExplicitTrust(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.8, 198.51.100.4")
	s := New()
	if got := s.remoteIP(req); got != "192.0.2.10" {
		t.Fatalf("untrusted proxy header used: %q", got)
	}
	s.TrustProxyHeaders = true
	if err := s.SetTrustedProxyCIDRs([]string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if got := s.remoteIP(req); got != "203.0.113.9" {
		t.Fatalf("trusted Cloudflare IP=%q", got)
	}
	req.Header.Del("CF-Connecting-IP")
	if got := s.remoteIP(req); got != "203.0.113.8" {
		t.Fatalf("trusted XFF IP=%q", got)
	}
	req.RemoteAddr = "198.51.100.10:1234"
	if got := s.remoteIP(req); got != "198.51.100.10" {
		t.Fatalf("unlisted direct peer spoofed XFF: %q", got)
	}
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := s.remoteIP(req); got != "192.0.2.10" {
		t.Fatalf("invalid trusted header used for rate key/logging: %q", got)
	}
}

func TestTrustedProxyCIDRsRejectInvalidOrEmptyConfiguration(t *testing.T) {
	s := New()
	if err := s.SetTrustedProxyCIDRs(nil); err == nil {
		t.Fatal("empty trusted proxy set accepted")
	}
	if err := s.SetTrustedProxyCIDRs([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid trusted proxy CIDR accepted")
	}
}

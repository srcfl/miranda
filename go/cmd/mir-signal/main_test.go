package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

// TestWithStaticForwardsSignalingPaths guards the production --webroot wiring: every
// path the signal server owns must be forwarded to it, not 404'd into the static
// file server. /registry shipped registered in signal.Server.Handler() but MISSING
// from withStatic's signalPaths map, so it 404'd in production (tests that used
// Server.Handler() directly never saw the wrapper). This pins the wrapper itself.
func TestWithStaticForwardsSignalingPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(withStatic(signal.New().Handler(), dir))
	defer ts.Close()

	// /registry MUST be forwarded to the signal server (handleRegistry returns a JSON
	// array for a wallet query) — not 404'd into the static FS.
	resp, err := http.Get(ts.URL + "/registry?wallet=test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		t.Fatalf("/registry via withStatic = %d %q; want 200 JSON array (forwarded to signal, not static 404)", resp.StatusCode, body)
	}

	// /healthz is forwarded too.
	if r, err := http.Get(ts.URL + "/healthz"); err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("/healthz via withStatic not forwarded (err=%v)", err)
	}

	// A missing static asset still 404s via the FS — proves we aren't trivially
	// forwarding everything to the signal server.
	if r, err := http.Get(ts.URL + "/vendor/missing-asset.js"); err != nil || r.StatusCode != http.StatusNotFound {
		t.Fatalf("a missing static asset should 404 via the FS (got err=%v)", err)
	}
}

func TestNewHTTPServerSetsTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(":0", handler)

	if srv.Addr != ":0" {
		t.Fatalf("addr: want :0, got %q", srv.Addr)
	}
	if srv.Handler != handler {
		t.Fatal("handler was not preserved")
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout: got %v", srv.ReadHeaderTimeout)
	}
	// ReadTimeout/WriteTimeout MUST be 0: they are whole-connection deadlines
	// that would cut the long-lived signaling + attach WebSockets mid-stream.
	if srv.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout must be 0 (would cut WebSockets), got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout must be 0 (would cut WebSockets), got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout: got %v", srv.IdleTimeout)
	}
}

func TestWithStaticAppliesBrowserSecurityHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>tr</title>"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := withStatic(http.NotFoundHandler(), dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing CSP")
	}
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "connect-src 'self'", "upgrade-insecure-requests"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
	// connect-src must default to 'self' only — never the old "https: wss:" wildcard,
	// which would let a tampered SPA beacon the owner key to any host.
	if strings.Contains(csp, "https:") || strings.Contains(csp, "wss:") {
		t.Fatalf("connect-src must not contain an https:/wss: wildcard by default: %q", csp)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := rr.Header().Get("Permissions-Policy"); !strings.Contains(got, "camera=(self)") || !strings.Contains(got, "microphone=()") {
		t.Fatalf("Permissions-Policy = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestCSPConnectSrcHonorsEnvOverride(t *testing.T) {
	want := "'self' https://relay.example.net wss://relay.example.net"
	t.Setenv("MIR_CSP_CONNECT_SRC", want)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>tr</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	withStatic(http.NotFoundHandler(), dir).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src "+want) {
		t.Fatalf("CSP %q missing operator connect-src %q", csp, want)
	}
}

func TestWithStaticKeepsSignalingHandlersSeparate(t *testing.T) {
	dir := t.TempDir()
	hit := false
	sig := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	withStatic(sig, dir).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/attach", nil))
	if !hit {
		t.Fatal("signaling handler was not called")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("static CSP should not be applied to signaling websocket route, got %q", got)
	}
}

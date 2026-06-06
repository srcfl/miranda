package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "connect-src 'self' https: wss:"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
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

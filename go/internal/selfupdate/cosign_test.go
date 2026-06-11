package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newBlobServer serves dummy signature/cert bytes so fetch() succeeds in tests
// that need to reach the cosign invocation. Content is irrelevant — the stub
// cosign on PATH decides pass/fail.
func newBlobServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sig":
			_, _ = w.Write([]byte("dummy-signature"))
		case "/pem":
			_, _ = w.Write([]byte("-----BEGIN CERTIFICATE-----\ndummy\n-----END CERTIFICATE-----\n"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCosignIdentityRegexp(t *testing.T) {
	got := cosignIdentityRegexp("srcfl/miranda")
	want := "https://github.com/srcfl/miranda/.github/workflows/release.yml@refs/tags/v.*"
	if got != want {
		t.Fatalf("identity regexp = %q, want %q", got, want)
	}
}

// TestVerifyChecksumsSignatureNoCosign pins the graceful-degradation contract:
// with cosign absent from PATH, verification must return nil (fall back to
// checksum-only) and emit exactly one warning. We force "cosign not found" by
// pointing PATH at an empty dir for the duration of the test.
func TestVerifyChecksumsSignatureNoCosign(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cosign on this PATH
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign unexpectedly resolvable on stripped PATH")
	}

	var warnings []string
	c := &Client{Repo: "srcfl/miranda"}
	rel := &Release{ChecksumsSigURL: "http://x/sig", ChecksumsCertURL: "http://x/pem"}
	if err := c.verifyChecksumsSignature(rel, []byte("sums"), func(m string) { warnings = append(warnings, m) }); err != nil {
		t.Fatalf("expected nil (fallback) when cosign absent, got %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cosign not found") {
		t.Fatalf("expected one 'cosign not found' warning, got %v", warnings)
	}
}

// TestVerifyChecksumsSignatureUnsignedRelease pins that a release WITHOUT
// signing assets (empty .sig/.pem URLs, e.g. a legacy tag) falls back rather
// than hard-failing — even when cosign IS installed. We fake a cosign on PATH so
// the LookPath check passes; it must never be invoked on this path.
func TestVerifyChecksumsSignatureUnsignedRelease(t *testing.T) {
	dir := t.TempDir()
	fakeCosign := filepath.Join(dir, "cosign")
	// A cosign that always FAILS — proves we never call it on the fallback path.
	if err := os.WriteFile(fakeCosign, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var warnings []string
	c := &Client{Repo: "srcfl/miranda"}
	rel := &Release{} // no ChecksumsSigURL / ChecksumsCertURL
	if err := c.verifyChecksumsSignature(rel, []byte("sums"), func(m string) { warnings = append(warnings, m) }); err != nil {
		t.Fatalf("expected nil (fallback) for unsigned release, got %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no cosign signature") {
		t.Fatalf("expected one 'no cosign signature' warning, got %v", warnings)
	}
}

// TestVerifyChecksumsSignatureFailsLoudly pins that when cosign IS present and
// verification fails, Apply's precondition surfaces an error (no silent pass).
// A stub cosign that exits non-zero stands in for a tampered checksums.txt.
func TestVerifyChecksumsSignatureFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	fakeCosign := filepath.Join(dir, "cosign")
	if err := os.WriteFile(fakeCosign, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	// Serve the .sig/.pem so fetch() succeeds and we reach the cosign call.
	srv := newBlobServer(t)
	defer srv.Close()

	c := &Client{Repo: "srcfl/miranda", HTTP: srv.Client()}
	rel := &Release{ChecksumsSigURL: srv.URL + "/sig", ChecksumsCertURL: srv.URL + "/pem"}
	err := c.verifyChecksumsSignature(rel, []byte("sums"), nil)
	if err == nil {
		t.Fatal("expected error when cosign verification fails, got nil")
	}
	if !strings.Contains(err.Error(), "cosign verify-blob failed") {
		t.Fatalf("expected cosign failure error, got %v", err)
	}
}

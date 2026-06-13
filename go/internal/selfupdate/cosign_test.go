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
// with cosign absent from PATH, verification returns nil (checksum-only fallback)
// and stays SILENT — a successful update must not nag the majority who have no
// cosign. We force "cosign not found" by pointing PATH at an empty dir.
func TestVerifyChecksumsSignatureNoCosign(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cosign on this PATH
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign unexpectedly resolvable on stripped PATH")
	}

	var notes []string
	c := &Client{Repo: "srcfl/miranda"}
	rel := &Release{ChecksumsSigURL: "http://x/sig", ChecksumsCertURL: "http://x/pem"}
	if err := c.verifyChecksumsSignature(rel, []byte("sums"), func(m string) { notes = append(notes, m) }); err != nil {
		t.Fatalf("expected nil (fallback) when cosign absent, got %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("expected NO output when cosign is absent (don't nag), got %v", notes)
	}
}

// TestVerifyChecksumsSignatureStrictRequiresCosign: with MIR_REQUIRE_COSIGN set,
// a missing cosign becomes a hard error so an operator can MANDATE provenance.
func TestVerifyChecksumsSignatureStrictRequiresCosign(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MIR_REQUIRE_COSIGN", "1")
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign unexpectedly resolvable on stripped PATH")
	}
	c := &Client{Repo: "srcfl/miranda"}
	rel := &Release{ChecksumsSigURL: "http://x/sig", ChecksumsCertURL: "http://x/pem"}
	err := c.verifyChecksumsSignature(rel, []byte("sums"), nil)
	if err == nil || !strings.Contains(err.Error(), "MIR_REQUIRE_COSIGN") {
		t.Fatalf("expected a hard error under MIR_REQUIRE_COSIGN, got %v", err)
	}
}

// TestVerifyChecksumsSignatureUnsignedRelease pins that a release WITHOUT
// signing assets (empty .sig/.pem URLs, e.g. a legacy tag) falls back silently
// rather than hard-failing — even when cosign IS installed. We fake a cosign on
// PATH so the LookPath check passes; it must never be invoked on this path.
func TestVerifyChecksumsSignatureUnsignedRelease(t *testing.T) {
	dir := t.TempDir()
	fakeCosign := filepath.Join(dir, "cosign")
	// A cosign that always FAILS — proves we never call it on the fallback path.
	if err := os.WriteFile(fakeCosign, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	var notes []string
	c := &Client{Repo: "srcfl/miranda"}
	rel := &Release{} // no ChecksumsSigURL / ChecksumsCertURL
	if err := c.verifyChecksumsSignature(rel, []byte("sums"), func(m string) { notes = append(notes, m) }); err != nil {
		t.Fatalf("expected nil (fallback) for unsigned release, got %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("expected NO output for an unsigned release, got %v", notes)
	}
}

// TestVerifyChecksumsSignaturePassEmitsNote: when cosign IS present and verifies,
// the update emits a positive one-line confirmation (and returns nil).
func TestVerifyChecksumsSignaturePassEmitsNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	srv := newBlobServer(t)
	defer srv.Close()

	var notes []string
	c := &Client{Repo: "srcfl/miranda", HTTP: srv.Client()}
	rel := &Release{ChecksumsSigURL: srv.URL + "/sig", ChecksumsCertURL: srv.URL + "/pem"}
	if err := c.verifyChecksumsSignature(rel, []byte("sums"), func(m string) { notes = append(notes, m) }); err != nil {
		t.Fatalf("expected nil when cosign verifies, got %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "verified") {
		t.Fatalf("expected one positive 'verified' note, got %v", notes)
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

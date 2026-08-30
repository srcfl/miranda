package selfupdate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stableClient is a network-free Client for exercising the cache helpers.
func stableClient() *Client {
	return &Client{Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64"}
}

func preClient() *Client {
	c := stableClient()
	c.Pre = true
	return c
}

func TestShouldCheckThrottle(t *testing.T) {
	c := stableClient()
	cache := filepath.Join(t.TempDir(), "update-check.json")
	// No file yet -> should check.
	if !c.shouldCheck(cache, time.Hour) {
		t.Fatal("expected check when no cache exists")
	}
	// Record a check "now"; within the window -> should not check.
	if err := c.writeCheck(cache, "v0.2.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if c.shouldCheck(cache, time.Hour) {
		t.Fatal("expected no check within throttle window")
	}
	// Backdate it past the window -> should check again.
	if err := c.writeCheck(cache, "v0.2.0", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !c.shouldCheck(cache, time.Hour) {
		t.Fatal("expected check after throttle window elapsed")
	}
}

func TestCachedLatest(t *testing.T) {
	c := stableClient()
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = c.writeCheck(cache, "v0.9.0", time.Now())
	if got := c.cachedLatest(cache); got != "v0.9.0" {
		t.Fatalf("cachedLatest=%q", got)
	}
	if got := c.cachedLatest(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Fatalf("expected empty for missing cache, got %q", got)
	}
	_ = os.Remove(cache)
}

// TestCacheIsChannelScoped: a cache written on one channel must not feed the
// other — a stable build must never nag about a beta it would refuse to
// install, and a beta build must not trust a stale stable answer.
func TestCacheIsChannelScoped(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = preClient().writeCheck(cache, "v0.8.0-beta.1", time.Now())

	if got := stableClient().cachedLatest(cache); got != "" {
		t.Fatalf("stable read a pre-channel cache: %q", got)
	}
	if !stableClient().shouldCheck(cache, time.Hour) {
		t.Fatal("stable must re-check over a pre-channel cache")
	}
	if got := preClient().cachedLatest(cache); got != "v0.8.0-beta.1" {
		t.Fatalf("pre channel lost its own cache: %q", got)
	}

	// A legacy cache with no channel field reads as stable.
	_ = os.WriteFile(cache, []byte(`{"latest_tag":"v0.7.0","checked_at":"2026-08-29T00:00:00Z"}`), 0o644)
	if got := stableClient().cachedLatest(cache); got != "v0.7.0" {
		t.Fatalf("legacy cache should read as stable, got %q", got)
	}
	if got := preClient().cachedLatest(cache); got != "" {
		t.Fatalf("pre channel read a legacy stable cache: %q", got)
	}
}

// TestMaybeNotifyDoesNotBlockOnNetwork pins the spec requirement that the notice
// "never delays a command": the cached newer version is surfaced immediately and
// the (stale-window) network refresh is backgrounded. The fake API blocks until
// the test releases it — if MaybeNotify did the refresh synchronously this call
// would hang and the test would time out.
func TestMaybeNotifyDoesNotBlockOnNetwork(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block the handler until the test allows it to return
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	defer close(release)

	cache := filepath.Join(t.TempDir(), "update-check.json")
	c := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}
	// Stale cache so shouldCheck() is true and the background refresh fires.
	_ = c.writeCheck(cache, "v0.2.0", time.Now().Add(-48*time.Hour))

	var buf bytes.Buffer
	// Would deadlock here if MaybeNotify blocked on the (blocked) server.
	c.MaybeNotify(&buf, cache, "0.1.0", time.Hour)

	if !strings.Contains(buf.String(), "v0.2.0") {
		t.Fatalf("expected cached notice surfaced immediately, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "run: mir update") {
		t.Fatalf("notice should point at `mir update`, got %q", buf.String())
	}
}

func TestMaybeNotifyHonorsOptOut(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	c := stableClient()
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = c.writeCheck(cache, "v9.9.9", time.Now())

	var buf bytes.Buffer
	c.MaybeNotify(&buf, cache, "0.1.0", time.Hour)
	if buf.Len() != 0 {
		t.Fatalf("expected no output when opted out, got %q", buf.String())
	}
}

// TestNoticeNowChecksForeground: with a stale cache, NoticeNow performs one
// bounded check right now, caches it, and prints the notice — the behaviour the
// no-argument guide relies on ("run `mir`, learn there is an update").
func TestNoticeNowChecksForeground(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/srcfl/miranda/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.9.0","assets":[
			{"name":"mir_0.9.0_linux_amd64.tar.gz","browser_download_url":"u"},
			{"name":"checksums.txt","browser_download_url":"u"}]}`))
	}))
	defer srv.Close()

	cache := filepath.Join(t.TempDir(), "update-check.json")
	c := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}

	var buf bytes.Buffer
	c.NoticeNow(&buf, cache, "0.8.0", time.Hour, time.Second)
	if !strings.Contains(buf.String(), "0.8.0 → v0.9.0") || !strings.Contains(buf.String(), "run: mir update") {
		t.Fatalf("expected a foreground notice, got %q", buf.String())
	}
	// The check is cached: a fresh cache answers without the network.
	srv.Close()
	buf.Reset()
	c.NoticeNow(&buf, cache, "0.8.0", time.Hour, time.Second)
	if !strings.Contains(buf.String(), "v0.9.0") {
		t.Fatalf("expected the cached notice, got %q", buf.String())
	}
}

func TestNoticeNowSilentOnNetworkFailure(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{APIBase: "http://127.0.0.1:1", Repo: "srcfl/miranda", Binary: "mir",
		OS: "linux", Arch: "amd64", HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	c.NoticeNow(&buf, filepath.Join(t.TempDir(), "update-check.json"), "0.8.0", time.Hour, 200*time.Millisecond)
	if buf.Len() != 0 {
		t.Fatalf("expected silence on failure, got %q", buf.String())
	}
}

func TestNoticeNowHonorsOptOut(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	c := stableClient()
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = c.writeCheck(cache, "v9.9.9", time.Now())
	var buf bytes.Buffer
	c.NoticeNow(&buf, cache, "0.1.0", time.Hour, time.Second)
	if buf.Len() != 0 {
		t.Fatalf("expected no output when opted out, got %q", buf.String())
	}
}

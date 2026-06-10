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

func TestShouldCheckThrottle(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update-check.json")
	// No file yet -> should check.
	if !shouldCheck(cache, time.Hour) {
		t.Fatal("expected check when no cache exists")
	}
	// Record a check "now"; within the window -> should not check.
	if err := writeCheck(cache, "v0.2.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if shouldCheck(cache, time.Hour) {
		t.Fatal("expected no check within throttle window")
	}
	// Backdate it past the window -> should check again.
	if err := writeCheck(cache, "v0.2.0", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !shouldCheck(cache, time.Hour) {
		t.Fatal("expected check after throttle window elapsed")
	}
}

func TestCachedLatest(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = writeCheck(cache, "v0.9.0", time.Now())
	if got := cachedLatest(cache); got != "v0.9.0" {
		t.Fatalf("cachedLatest=%q", got)
	}
	if got := cachedLatest(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Fatalf("expected empty for missing cache, got %q", got)
	}
	_ = os.Remove(cache)
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
	// Stale cache so shouldCheck() is true and the background refresh fires.
	_ = writeCheck(cache, "v0.2.0", time.Now().Add(-48*time.Hour))

	var buf bytes.Buffer
	c := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}
	// Would deadlock here if MaybeNotify blocked on the (blocked) server.
	c.MaybeNotify(&buf, cache, "0.1.0", time.Hour)

	if !strings.Contains(buf.String(), "v0.2.0") {
		t.Fatalf("expected cached notice surfaced immediately, got %q", buf.String())
	}
}

func TestMaybeNotifyHonorsOptOut(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = writeCheck(cache, "v9.9.9", time.Now())

	var buf bytes.Buffer
	c := New("srcfl/miranda", "mir")
	c.MaybeNotify(&buf, cache, "0.1.0", time.Hour)
	if buf.Len() != 0 {
		t.Fatalf("expected no output when opted out, got %q", buf.String())
	}
}

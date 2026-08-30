package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/client"
)

// fakeRelay serves the two directory endpoints a warm-up asks for and counts the
// requests, so a test can prove a warm run makes none.
type fakeRelay struct {
	*httptest.Server
	mu      sync.Mutex
	hits    int
	entries []map[string]string
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	r := &fakeRelay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits++
		entries := append([]map[string]string(nil), r.entries...)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/revocations":
			_, _ = w.Write([]byte("[]"))
		case "/registry":
			_ = json.NewEncoder(w).Encode(entries)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *fakeRelay) hitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits
}

func (r *fakeRelay) publish(t *testing.T, id *client.Identity, m client.Machine) {
	t.Helper()
	blob, _, err := client.SealRegistryMachine(id, m)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, map[string]string{"machine_id": m.MachineID, "blob": blob})
}

// A second `mir ls` inside the TTL answers from the cache: no relay round trips
// at all, and the same machines.
func TestListWarmCacheMakesNoSecondRoundTrip(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	relay := newFakeRelay(t)
	t.Setenv("MIR_SIGNAL", relay.URL)
	dir := t.TempDir()

	var out, errb bytes.Buffer
	add := []string{"add-machine", "--dir", dir, "--name", "box", "--id", "m1",
		"--host-pub", "aabbcc", "--signal", relay.URL}
	if code := Run(add, &out, &errb); code != 0 {
		t.Fatalf("add exit = %d, stderr = %q", code, errb.String())
	}
	out.Reset()
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("cold list exit = %d, stderr = %q", code, errb.String())
	}
	cold := relay.hitCount()
	if cold == 0 {
		t.Fatal("a cold list must ask the relay")
	}
	out.Reset()
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("warm list exit = %d, stderr = %q", code, errb.String())
	}
	if relay.hitCount() != cold {
		t.Fatalf("warm list made %d extra relay requests, want 0", relay.hitCount()-cold)
	}
	if !strings.Contains(out.String(), "box") {
		t.Fatalf("warm list = %q", out.String())
	}
}

// Relay unreachable with machines saved locally: the list still works and says
// so in plain words (N3 copy), rather than passing a stale list off as live.
func TestListSaysDiscoveryIsPausedWhenTheRelayIsUnreachable(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	t.Setenv("MIR_SIGNAL", "http://127.0.0.1:1")
	dir := t.TempDir()

	var out, errb bytes.Buffer
	add := []string{"add-machine", "--dir", dir, "--name", "box", "--id", "m1",
		"--host-pub", "aabbcc", "--signal", "http://127.0.0.1:1"}
	if code := Run(add, &out, &errb); code != 0 {
		t.Fatalf("add exit = %d", code)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), discoveryPausedNote) {
		t.Fatalf("stderr = %q, want the discovery-paused note", errb.String())
	}
	if !strings.Contains(out.String(), "box") {
		t.Fatalf("list = %q, want the saved machine", out.String())
	}
}

// A machine paired on another device seconds ago is not in this device's fresh
// cache. Before calling the name unknown, the client asks the relay once.
func TestResolveAsksTheRelayBeforeCallingANameUnknown(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	relay := newFakeRelay(t)
	t.Setenv("MIR_SIGNAL", relay.URL)
	dir := t.TempDir()

	idn, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	a := &app{out: &out, errOut: &errb, binary: "mir"}

	// Warm the cache while the registry is empty.
	if _, err := a.resolveMachines(context.Background(), dir, nil, idn); err != nil {
		t.Fatal(err)
	}
	// Another device pairs "loft" a moment later.
	relay.publish(t, idn, client.Machine{Name: "loft", MachineID: "m-loft", HostPubHex: "bb22", SignalURL: relay.URL})

	resolved, err := a.resolveMachines(context.Background(), dir, []string{"loft"}, idn)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[0].MachineID != "m-loft" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

// The unknown-machine copy is unchanged, in both the online and the offline case.
func TestResolveUnknownMachineCopy(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	relay := newFakeRelay(t)
	t.Setenv("MIR_SIGNAL", relay.URL)
	dir := t.TempDir()
	idn, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	a := &app{out: &out, errOut: &errb, binary: "mir"}

	_, err = a.resolveMachines(context.Background(), dir, []string{"ghost"}, idn)
	if err == nil || !strings.Contains(err.Error(), "neither paired locally nor online") {
		t.Fatalf("online error = %v", err)
	}

	relay.Close()
	_, err = a.resolveMachines(context.Background(), dir, []string{"ghost"}, idn)
	if err == nil || !strings.Contains(err.Error(), "the relay was unreachable") {
		t.Fatalf("offline error = %v", err)
	}
}

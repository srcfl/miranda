package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
)

// TestDoctorShareRedactsPrivateMaterial is B2's hard requirement: redaction is
// enforced by tests, not care. It seeds state with recognizable values — an
// owner identity, a machine id, a machine name, a custom relay URL, real state
// paths — and asserts none of them survive into `mir doctor --share` output.
func TestDoctorShareRedactsPrivateMaterial(t *testing.T) {
	clientDir := t.TempDir()
	agentDir := t.TempDir()
	if _, err := client.LoadOrCreateIdentity(clientDir); err != nil {
		t.Fatal(err)
	}
	storage, err := client.InspectIdentityStorage(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	// Non-HTTPS, non-loopback: trips the "agent relay URL is unsafe" line,
	// which must print `custom`, never this hostname.
	cfg, err := agent.LoadOrInit(agentDir, "SECRET-MACHINE-NAME", "http://private.internal.example")
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	var out, errOut bytes.Buffer
	Run([]string{"doctor", "--share", "--client-dir", clientDir, "--agent-dir", agentDir, "--signal", relay.URL}, &out, &errOut)
	report := out.String()

	for name, secret := range map[string]string{
		"owner id":         storage.OwnerID,
		"owner id prefix":  storage.OwnerID[:8],
		"machine id":       cfg.MachineID,
		"machine name":     "SECRET-MACHINE-NAME",
		"custom agent URL": "private.internal.example",
		"client dir path":  clientDir,
		"agent dir path":   agentDir,
	} {
		if strings.Contains(report, secret) {
			t.Errorf("--share leaked the %s (%q):\n%s", name, secret, report)
		}
	}
	for _, want := range []string{"platform: ", "checks:", "(redacted)", "custom"} {
		if !strings.Contains(report, want) {
			t.Errorf("--share output missing %q:\n%s", want, report)
		}
	}
}

// A custom relay that fails its health check must not leak through the HTTP
// error string either (Go's client embeds the dialed URL in the error).
func TestDoctorShareScrubsCustomURLFromHealthError(t *testing.T) {
	var out, errOut bytes.Buffer
	Run([]string{"doctor", "--share",
		"--client-dir", t.TempDir(), "--agent-dir", t.TempDir(),
		"--signal", "http://127.0.0.1:1"}, &out, &errOut)
	report := out.String()
	if strings.Contains(report, "127.0.0.1:1") {
		t.Fatalf("--share leaked the custom relay URL through the health error:\n%s", report)
	}
	if !strings.Contains(report, "relay health check failed: ") {
		t.Fatalf("expected the health failure line:\n%s", report)
	}
}

// Plain `mir doctor` (no --share) keeps its verbatim, local-only output.
func TestDoctorWithoutShareStaysVerbatim(t *testing.T) {
	clientDir := t.TempDir()
	var out, errOut bytes.Buffer
	Run([]string{"doctor", "--client-dir", clientDir,
		"--agent-dir", t.TempDir(), "--offline"}, &out, &errOut)
	if !strings.Contains(out.String(), clientDir) {
		t.Fatalf("plain doctor should name the real path:\n%s", out.String())
	}
}

func TestScrubHomeReplacesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory in this environment")
	}
	got := scrubHome("cannot stat " + home + "/x/owner.json: permission denied")
	if strings.Contains(got, home) || !strings.Contains(got, "~/x/owner.json") {
		t.Fatalf("scrubHome left the home path: %q", got)
	}
}

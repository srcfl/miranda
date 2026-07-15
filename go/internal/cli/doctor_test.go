package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
)

func TestDoctorValidatesHealthyLocalStateAndRelay(t *testing.T) {
	clientDir := t.TempDir()
	agentDir := t.TempDir()
	if _, err := client.LoadOrCreateIdentity(clientDir); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadOrInit(agentDir, "box", "http://localhost"); err != nil {
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
	code := Run([]string{"doctor", "--client-dir", clientDir, "--agent-dir", agentDir, "--signal", relay.URL}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, want := range []string{"owner root is available", "agent machine identity", "relay is healthy", "no blocking failures"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorRejectsLegacyPlaintextOwnerRoot(t *testing.T) {
	clientDir := t.TempDir()
	legacy := `{"secret":"` + strings.Repeat("ab", 32) + `","owner_pub":"` + strings.Repeat("cd", 32) + `"}`
	if err := os.WriteFile(filepath.Join(clientDir, "owner.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run([]string{"doctor", "--client-dir", clientDir, "--agent-dir", filepath.Join(t.TempDir(), "absent"), "--offline"}, &out, &errOut)
	if code == 0 {
		t.Fatalf("plaintext identity passed doctor:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "plaintext root") || !strings.Contains(errOut.String(), "doctor found") {
		t.Fatalf("unexpected output stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

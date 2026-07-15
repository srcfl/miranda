package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
)

func TestDefaultStateSeparatesOwnerFromTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clientDir := defaultClientDir()
	agentDir := defaultAgentDir()
	if clientDir == agentDir {
		t.Fatal("client and agent state directories must be separate")
	}
	if !strings.HasSuffix(clientDir, filepath.Join(".miranda", "client")) || !strings.HasSuffix(agentDir, filepath.Join(".miranda", "agent")) {
		t.Fatalf("unexpected state layout: client=%q agent=%q", clientDir, agentDir)
	}
	if _, err := client.LoadOrCreateIdentity(clientDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureAgentOnlyDir(clientDir); err == nil {
		t.Fatal("agent must refuse a directory containing the owner identity")
	}
	if err := ensureAgentOnlyDir(agentDir); err != nil {
		t.Fatalf("fresh agent directory rejected: %v", err)
	}
}

// Agent startup depends only on its machine key, owner pins, and opaque
// records. It must not create or load owner.json as a side effect.
func TestAgentConfigContainsNoOwnerIdentity(t *testing.T) {
	dir := t.TempDir()
	if _, err := agent.LoadOrInit(dir, "machine", "https://relay.example"); err != nil {
		t.Fatal(err)
	}
	if client.IdentityExists(dir) {
		t.Fatal("agent initialization created owner.json")
	}
	if err := agent.ProvisionOwner(dir, "owner-id", "opaque-record", "public-registration-auth"); err != nil {
		t.Fatal(err)
	}
	if client.IdentityExists(dir) {
		t.Fatal("owner provisioning created owner.json")
	}
}

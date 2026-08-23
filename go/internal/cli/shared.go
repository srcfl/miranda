package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

const repoSlug = "srcfl/miranda"

func stateRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".miranda")
}

func defaultClientDir() string { return filepath.Join(stateRoot(), "client") }
func defaultAgentDir() string  { return filepath.Join(stateRoot(), "agent") }

// ensureAgentOnlyDir prevents a target machine from accidentally sharing the
// directory that contains an owner's mesh-wide client identity. A compromised
// target must yield only that target's device key.
func ensureAgentOnlyDir(dir string) error {
	if client.IdentityExists(dir) {
		return fmt.Errorf("refusing agent state directory %q: it contains owner.json; use a separate --dir (default: %s)", dir, defaultAgentDir())
	}
	return nil
}

func updateCachePath(dir string) string { return filepath.Join(dir, "update-check.json") }

// freshSetup reports whether the default config dir holds no mir state yet, so the
// no-argument guide can lead with a one-time welcome.
func freshSetup() bool {
	for _, path := range []string{
		filepath.Join(defaultClientDir(), "owner.json"),
		filepath.Join(defaultClientDir(), "machines.json"),
		filepath.Join(defaultAgentDir(), "config.json"),
	} {
		if _, err := os.Stat(path); err == nil {
			return false
		}
	}
	return true
}

func looksLikeX25519Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "machine"
	}
	return h
}

// iceFlags registers --stun/--turn/--turn-user/--turn-pass on fs and returns a
// closure building the ICE server list (call after fs.Parse). TURN is the opt-in
// symmetric-NAT fallback; Noise keeps it blind to content.
func iceFlags(fs *flag.FlagSet) func() []peer.ICEServer {
	stun := fs.String("stun", defaults.STUNURL(), "comma-separated STUN URLs (empty disables); default is ours")
	turn := fs.String("turn", "", "comma-separated TURN URLs (opt-in fallback; e.g. turn:host:3478)")
	user := fs.String("turn-user", "", "TURN username")
	pass := fs.String("turn-pass", "", "TURN password")
	return func() []peer.ICEServer {
		var servers []peer.ICEServer
		if s := splitCSV(*stun); len(s) > 0 {
			servers = append(servers, peer.ICEServer{URLs: s})
		}
		if t := splitCSV(*turn); len(t) > 0 {
			servers = append(servers, peer.ICEServer{URLs: t, Username: *user, Credential: *pass})
		}
		return servers
	}
}

// splitCSV splits a comma-separated flag into a trimmed slice; empty -> nil.
func iceHasTURN(servers []peer.ICEServer) bool {
	for _, s := range servers {
		if s.Username != "" || s.Credential != "" {
			return true
		}
	}
	return false
}

func iceSTUNURLs(servers []peer.ICEServer) []string {
	var out []string
	for _, s := range servers {
		if s.Username == "" && s.Credential == "" {
			out = append(out, s.URLs...)
		}
	}
	return out
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// parsePrefix turns a key spec like "ctrl-o", "c-a", "^o", or "ctrl-space" into
// its control byte and a human label for the hint.
func parsePrefix(s string) (byte, string, error) {
	x := strings.ToLower(strings.TrimSpace(s))
	x = strings.TrimPrefix(x, "ctrl-")
	x = strings.TrimPrefix(x, "c-")
	x = strings.TrimPrefix(x, "^")
	switch x {
	case "space":
		return 0x00, "Ctrl-Space", nil
	case "]":
		return 0x1d, "Ctrl-]", nil
	}
	if len(x) == 1 && x[0] >= 'a' && x[0] <= 'z' {
		return x[0] & 0x1f, "Ctrl-" + strings.ToUpper(x), nil
	}
	return 0, "", fmt.Errorf("bad --prefix %q (use e.g. ctrl-o, ctrl-a, ctrl-space)", s)
}

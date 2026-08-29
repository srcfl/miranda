package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/identity"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/version"
)

type doctorReport struct {
	w     func(string, ...any)
	share bool
	fails int
	warns int
}

func (d *doctorReport) ok(format string, args ...any) { d.w("ok    "+format+"\n", args...) }
func (d *doctorReport) warn(format string, args ...any) {
	d.warns++
	d.w("warn  "+format+"\n", args...)
}
func (d *doctorReport) fail(format string, args ...any) {
	d.fails++
	d.w("FAIL  "+format+"\n", args...)
}

func (a *app) cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	clientDir := fs.String("client-dir", defaultClientDir(), "client state directory")
	agentDir := fs.String("agent-dir", defaultAgentDir(), "agent state directory")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling relay base URL")
	offline := fs.Bool("offline", false, "skip relay health check")
	share := fs.Bool("share", false, "print a paste-safe report for a public issue (no identities, names, paths, or custom URLs)")
	_ = fs.Parse(args)
	d := &doctorReport{
		share: *share,
		w:     func(format string, values ...any) { fmt.Fprintf(a.out, format, values...) },
	}
	if *share {
		// Belt and suspenders: known-sensitive values go through id/dir/url
		// below, and every line is additionally scrubbed of the home directory
		// so an OS error that embeds a path cannot leak the username.
		d.w = func(format string, values ...any) {
			fmt.Fprint(a.out, scrubHome(fmt.Sprintf(format, values...)))
		}
		d.shareHeader()
	}

	d.checkClient(*clientDir)
	d.checkAgent(*agentDir)
	if _, err := exec.LookPath("tmux"); err != nil {
		d.warn("tmux is not installed; Miranda can serve a shell but cannot preserve terminal sessions")
	} else {
		d.ok("tmux is available")
	}
	if *offline {
		d.warn("relay health check skipped (--offline)")
	} else {
		d.checkRelay(*signalURL)
	}
	if d.fails > 0 {
		return fmt.Errorf("doctor found %d security/availability failure(s) and %d warning(s)", d.fails, d.warns)
	}
	fmt.Fprintf(a.out, "ready: %d warning(s), no blocking failures\n", d.warns)
	return nil
}

func (d *doctorReport) checkClient(dir string) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		d.warn("client identity is not initialized (%s)", d.dir(dir))
		return
	}
	if err != nil {
		d.fail("cannot inspect client state: %v", err)
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		d.fail("client state directory permissions are %o, want 700 or stricter", info.Mode().Perm())
	} else {
		d.ok("client state directory permissions are private")
	}
	ownerPath := filepath.Join(dir, "owner.json")
	if !checkPrivateFile(d, ownerPath, "owner metadata") {
		return
	}
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		d.fail("cannot read owner metadata: %v", err)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		d.fail("owner metadata is invalid JSON: %v", err)
		return
	}
	if _, exposed := raw["secret"]; exposed {
		d.fail("owner.json still contains a plaintext root; run any identity command once to migrate it")
	}
	if _, exposed := raw["owner_priv"]; exposed {
		d.fail("owner.json contains a derived private key; rotate the legacy identity")
	}
	storage, err := client.InspectIdentityStorage(dir)
	if err != nil {
		d.fail("owner identity/keychain verification failed — if your keychain is locked, unlock it and re-run (cause: %v)", err)
	} else if storage.LegacyPlaintext {
		d.fail("owner identity uses legacy plaintext private material")
	} else {
		d.ok("owner root is available from %s and matches %s", storage.Backend, d.id(storage.OwnerID))
	}
	if _, err := client.ListRevocations(dir); err != nil {
		d.fail("local revocation store failed signature verification: %v", err)
	} else {
		d.ok("local signed revocation store verifies")
	}
}

func (d *doctorReport) checkAgent(dir string) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		d.warn("agent is not initialized (%s)", d.dir(dir))
		return
	}
	if err != nil {
		d.fail("cannot inspect agent state: %v", err)
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		d.fail("agent state directory permissions are %o, want 700 or stricter", info.Mode().Perm())
	} else {
		d.ok("agent state directory permissions are private")
	}
	if client.IdentityExists(dir) {
		d.fail("agent directory contains owner.json; targets must never hold the owner root")
	}
	configPath := filepath.Join(dir, "config.json")
	if !checkPrivateFile(d, configPath, "agent config") {
		return
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		d.fail("cannot read agent config: %v", err)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		d.fail("agent config is invalid JSON: %v", err)
		return
	}
	if _, bad := raw["secret"]; bad {
		d.fail("agent config contains an owner-root-shaped secret field")
	}
	if _, bad := raw["owner_priv"]; bad {
		d.fail("agent config contains an owner private key")
	}
	var cfg agent.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		d.fail("agent config cannot be decoded: %v", err)
		return
	}
	private, privateErr := hex.DecodeString(cfg.HostPrivHex)
	if privateErr != nil || len(private) != 32 {
		d.fail("agent host private key is missing or malformed")
	} else if derived, err := noise.PublicFromPrivate(private); err != nil || !bytes.Equal(derived, cfg.HostPub()) {
		d.fail("agent host public key does not match its private key")
	}
	if secret, err := hex.DecodeString(cfg.RegistrationSecret); err != nil || len(secret) != 32 {
		d.fail("agent registration secret is missing or malformed")
	}
	if !validDoctorMachineID(cfg.MachineID) {
		d.fail("agent machine id is missing or malformed")
	} else {
		d.ok("agent machine identity is structurally valid (%s)", d.id(cfg.MachineID))
	}
	commitment, commitmentErr := cfg.RegistrationCommitment()
	authFailed := false
	for _, owner := range cfg.PairedOwners {
		auth := cfg.OwnerRegistrationAuth[owner]
		signature, err := base64.StdEncoding.DecodeString(auth)
		if commitmentErr != nil || err != nil || identity.VerifyAuth(owner, identity.RegistrationChallenge(cfg.MachineID, commitment), signature) != nil {
			d.fail("agent owner %s lacks a valid registration authorization", d.id(owner))
			authFailed = true
		}
	}
	if len(cfg.PairedOwners) > 0 && !authFailed {
		d.ok("agent owner registration authorizations verify")
	}
	if cfg.SignalURL != "" {
		u, err := url.Parse(cfg.SignalURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
			d.fail("agent relay URL is unsafe: %q", d.shareURL(cfg.SignalURL))
		}
	}
}

func checkPrivateFile(d *doctorReport, path, label string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		d.warn("%s is absent (%s)", label, d.dir(path))
		return false
	}
	if err != nil {
		d.fail("cannot stat %s: %v", label, err)
		return false
	}
	if info.Mode().Perm()&0o077 != 0 {
		d.fail("%s permissions are %o, want 600 or stricter", label, info.Mode().Perm())
	} else {
		d.ok("%s permissions are private", label)
	}
	return true
}

func (d *doctorReport) checkRelay(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		d.fail("relay URL is invalid: %q", d.shareURL(rawURL))
		return
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		d.fail("relay URL must use HTTPS outside localhost: %s", d.shareURL(rawURL))
		return
	}
	ctxClient := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := ctxClient.Get(strings.TrimRight(rawURL, "/") + "/healthz")
	if err != nil {
		d.fail("relay health check failed: %s", d.scrubValue(err.Error(), rawURL))
		return
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		d.fail("relay health returned %s", response.Status)
		return
	}
	// Renames and revocations resolve by timestamp (last writer wins), so a
	// machine with a badly skewed clock silently loses every merge.
	if skew, ok := clockSkew(response.Header.Get("Date"), time.Now()); ok && skew > maxClockSkew {
		d.warn("this machine's clock is off by about %s — renames and revocations resolve by timestamp, so fix the system clock", skew.Round(time.Minute))
	}
	d.ok("relay is healthy over %s", strings.ToUpper(u.Scheme))
}

// maxClockSkew is how far the local clock may drift from the relay's Date
// header before doctor warns.
const maxClockSkew = 5 * time.Minute

// clockSkew compares an HTTP Date header with local time; ok=false when the
// header is absent or unparseable.
func clockSkew(dateHeader string, now time.Time) (time.Duration, bool) {
	t, err := http.ParseTime(dateHeader)
	if err != nil {
		return 0, false
	}
	skew := now.Sub(t)
	if skew < 0 {
		skew = -skew
	}
	return skew, true
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

// shareHeader opens the --share report: version, platform, and tmux — the
// context a public issue needs and nothing personal.
func (d *doctorReport) shareHeader() {
	d.w("mir %s\n", version.String())
	d.w("platform: %s/%s, %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	if out, err := exec.Command("tmux", "-V").Output(); err == nil {
		d.w("tmux: %s\n", strings.TrimSpace(string(out)))
	} else {
		d.w("tmux: not installed\n")
	}
	d.w("checks:\n")
}

// id renders identity-shaped values (owner ids, machine ids): shortened
// normally, withheld entirely in a --share report.
func (d *doctorReport) id(value string) string {
	if d.share {
		return "(redacted)"
	}
	return shortDoctorID(value)
}

// dir renders a state path: verbatim normally, basename-only in a --share
// report (an absolute path usually contains the username).
func (d *doctorReport) dir(path string) string {
	if d.share {
		return "…/" + filepath.Base(path)
	}
	return path
}

// shareURL renders a relay URL: verbatim normally; in a --share report only
// the default relay is shown, anything else becomes "custom" (a private URL
// can identify a home server).
func (d *doctorReport) shareURL(raw string) string {
	if d.share && raw != defaults.SignalURL() {
		return "custom"
	}
	return raw
}

// scrubValue blanks a custom relay URL out of an error string in a --share
// report. HTTP client errors embed both the URL they dialed and, separately,
// the host:port the dial resolved to — both must go.
func (d *doctorReport) scrubValue(s, rawURL string) string {
	if !d.share || rawURL == defaults.SignalURL() {
		return s
	}
	if base := strings.TrimRight(rawURL, "/"); base != "" {
		s = strings.ReplaceAll(s, base, "custom")
	}
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		s = strings.ReplaceAll(s, u.Host, "custom")
	}
	return s
}

// scrubHome replaces the user's home directory in s with "~" so no report
// line can leak the username through an embedded path.
func scrubHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return s
	}
	return strings.ReplaceAll(s, home, "~")
}

func shortDoctorID(value string) string {
	if len(value) > 12 {
		return value[:12] + "…"
	}
	return value
}

func validDoctorMachineID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

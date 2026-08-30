package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/noise"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/selfupdate"
	"github.com/srcful/terminal-relay/go/internal/version"
)

// identity loads the client owner identity (creating it on first use), printing a
// one-time intro when it was just created so a new user learns they have an owner
// identity and how to back it up. The intro goes to stderr, keeping command stdout
// clean for scripts.
func (a *app) identity(dir string) (*client.Identity, error) {
	fresh := !client.IdentityExists(dir)
	id, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		return nil, err
	}
	if fresh && id.HasRootedIdentity() {
		fmt.Fprintf(a.errOut, "✓ created your %s identity — %s\n", a.binary, id.OwnerID)
		fmt.Fprintf(a.errOut, "  recovery: %s identity export-recovery\n\n", a.binary)
	}
	return id, nil
}

// requireRootedIdentity returns a guided error when a legacy identity tries to
// attach, spelling out the one-time identity + re-pair migration so the user isn't
// surprised when re-pairing turns out to be necessary.
func (a *app) requireRootedIdentity(id *client.Identity) error {
	if id.HasRootedIdentity() {
		return nil
	}
	b := a.binary
	fmt.Fprintln(a.errOut, "This identity predates Miranda identity v2.")
	fmt.Fprintln(a.errOut)
	fmt.Fprintln(a.errOut, "Upgrade it (one-time):")
	fmt.Fprintln(a.errOut, "    "+b+" identity rotate --yes")
	fmt.Fprintln(a.errOut)
	fmt.Fprintln(a.errOut, "That creates a NEW owner id, so each machine you paired")
	fmt.Fprintln(a.errOut, "before must be re-paired: run `"+b+" pair` on the machine and `"+b+" pair <code>` here.")
	return fmt.Errorf("legacy identity — run `%s identity rotate --yes`", b)
}

// cmdSelfUpdate replaces the running binary with the latest GitHub Release
// (verified by SHA256) when a newer version exists. a.binary selects the asset
// (mir / mir-agent), so the deprecated shim updates its own binary.
func (a *app) cmdSelfUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	pre := fs.Bool("pre", false, "follow prereleases (the beta channel); a prerelease build does this by default")
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := updateClient(a.binary)
	if *pre {
		c.Pre = true
	}
	rel, err := c.Latest()
	if err != nil {
		return humanUpdateErr(err)
	}
	if !selfupdate.IsNewer(version.Version, rel.Tag) {
		fmt.Fprintf(a.out, "already up to date (%s)\n", version.Version)
		return nil
	}
	fmt.Fprintf(a.out, "updating %s %s → %s …\n", a.binary, version.Version, rel.Tag)
	if err := c.Apply(rel, exe); err != nil {
		return humanUpdateErr(err)
	}
	fmt.Fprintf(a.out, "updated %s → %s\n", a.binary, rel.Tag)
	return nil
}

// cmdRun attaches and runs one command non-interactively, streaming output for a
// short window. Useful for scripts and the NAT-sim smoke test (no TTY needed).
func (a *app) cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	ice := iceFlags(fs)
	window := fs.Duration("window", 3*time.Second, "how long to stream output before exiting")
	// run is the one command that does NOT accept flags after the positionals:
	// everything past <machine> is the remote command, so `mir run box ls -la`
	// must hand `-la` to ls, not to mir. Its own flags therefore come first,
	// which is what the usage line says.
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: %s run [flags] <machine> <command...>   (flags come first; everything after <machine> is the remote command)", a.binary)
	}
	name := rest[0]
	cmd := strings.Join(rest[1:], " ")

	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	machines, err := a.resolveMachines(context.Background(), *dir, []string{name}, idn)
	if err != nil {
		return err
	}
	m := &machines[0]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, ice())
	if err != nil {
		return humanAttachErr(a.binary, m.Name, err)
	}
	defer cleanup()
	if err := client.RunCommand(ctx, mc, sess, cmd, *window, os.Stdout); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (a *app) cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	legacyRotate := fs.Bool("wallet", false, "deprecated alias for identity rotation")
	_ = fs.Parse(args)
	id, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if *legacyRotate && !id.HasRootedIdentity() {
		if id, err = client.Rekey(*dir); err != nil {
			return err
		}
		fmt.Fprintln(a.errOut, "identity rotated — owner_id changed; re-pair your machines")
	}
	fmt.Fprintf(a.out, "owner public key:\n  %s\n", id.OwnerPubHex)
	if id.HasRootedIdentity() {
		fmt.Fprintf(a.out, "\nMiranda owner id:\n  %s\n\nPin it on each machine:\n  mir pair-dev --owner-id %s\n", id.OwnerID, id.OwnerID)
	}
	return nil
}

func (a *app) cmdAddMachine(args []string) error {
	fs := flag.NewFlagSet("add-machine", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	name := fs.String("name", "", "machine name")
	id := fs.String("id", "", "machine id (from `mir enroll`)")
	hostPub := fs.String("host-pub", "", "machine host public key (hex, from `mir enroll`)")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	_ = fs.Parse(args)
	if *name == "" || *id == "" || *hostPub == "" {
		return fmt.Errorf("--name, --id and --host-pub are required")
	}
	m := client.Machine{Name: *name, MachineID: *id, HostPubHex: strings.ToLower(*hostPub), SignalURL: *signalURL}
	if err := client.AddMachine(*dir, m); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "added machine %q (%s) via %s\n", m.Name, m.MachineID, m.SignalURL)
	return nil
}

func (a *app) cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	_ = fs.Parse(args)
	// Cheap, non-blocking update notice (cache-only display; refresh in background).
	updateClient(a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)
	client.SweepGuestState(*dir, time.Now()) // shares whose window closed age out here
	local, err := client.ListMachines(*dir)
	if err != nil {
		return err
	}

	// Discover your own machines from the relay's encrypted registry. Best-effort:
	// a legacy identity or a relay hiccup just falls back to the local list.
	// The registry is keyed by owner id on the default relay (the one agents register
	// with), so fetch there regardless of any per-machine SignalURL.
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	var discovered []client.Machine
	discoveredID := map[string]bool{}
	if idn.HasRootedIdentity() {
		warm := a.prewarm(context.Background(), *dir, idn, "", nil)
		if warm.RegistryErr != nil {
			// Degrading silently would show a stale list as if it were live.
			fmt.Fprintln(a.errOut, discoveryPausedNote)
		}
		discovered = warm.Discovered
		for _, m := range discovered {
			discoveredID[m.MachineID] = true
		}
		// One-line "new device joined" notice on stderr, so stdout stays script-clean.
		_ = client.NotifyNewDevices(a.errOut, *dir, discovered)
	}

	revocations, err := client.ListRevocations(*dir)
	if err != nil {
		return err
	}
	merged := client.FilterRevoked(client.MergeMachines(local, discovered), idn.OwnerID, revocations)
	if len(merged) == 0 {
		fmt.Fprintln(a.out, "no machines yet — run `mir up` on the machine you want to reach; its first run shows a pairing QR")
		return nil
	}
	for _, m := range merged {
		if m.Owner != "" {
			// A share someone gave this identity: the grant, not the registry,
			// says what it is and how long it lasts.
			detail := "shared with you"
			if g := client.GuestGrantFor(*dir, m.MachineID); g != nil {
				detail = fmt.Sprintf("shared with you · %s · %s", modeWord(g.Mode), expiryPhrase(g.NA, false, time.Now()))
			}
			fmt.Fprintf(a.out, "%-16s %s  %s\n", m.Name, m.MachineID, detail)
			continue
		}
		tag := ""
		if discoveredID[m.MachineID] {
			tag = "  (online)"
		}
		fmt.Fprintf(a.out, "%-16s %s  %s%s\n", m.Name, m.MachineID, m.SignalURL, tag)
	}
	return nil
}

// isCleanDetach reports whether err is a normal peer disconnect — the agent went
// away / closed the data channel (peer.ErrDataChannelClosed) or the stream hit
// io.EOF. The mux path already treats an all-sessions disconnect as a clean exit;
// this lets the single-machine branch match instead of printing "error: …" and
// exiting 1 on an ordinary detach.
func isCleanDetach(err error) bool {
	return errors.Is(err, peer.ErrDataChannelClosed) || errors.Is(err, io.EOF)
}

// prewarm runs the three pre-attach relay fetches — signed revocations, the
// encrypted registry, TURN credentials — concurrently and behind their caches
// (spec D2). turnURL empty asks for no credentials. Relay availability is not
// authority: already-stored tombstones are always enforced, and every newly
// fetched record is owner-verified before it lands.
func (a *app) prewarm(ctx context.Context, dir string, idn *client.Identity, turnURL string, stun []string) *client.Prewarm {
	warm := (&client.Prewarmer{Dir: dir}).Run(ctx, client.PrewarmRequest{
		Identity:     idn,
		SignalURL:    defaults.SignalURL(),
		TURNURL:      turnURL,
		STUNFallback: stun,
	})
	if warm.RevocationStoreErr != nil {
		fmt.Fprintf(a.errOut, "warning: could not cache signed revocation: %v\n", warm.RevocationStoreErr)
	}
	return warm
}

// resolveMachines resolves every name against the local pin set and the owner's
// encrypted live registry. This gives single attach, multi-attach, and
// non-interactive run identical zero-config discovery behavior.
func (a *app) resolveMachines(ctx context.Context, dir string, names []string, idn *client.Identity) ([]client.Machine, error) {
	resolved, _, err := a.resolveMachinesWarm(ctx, dir, names, idn, "", nil)
	return resolved, err
}

// resolveMachinesWarm is resolveMachines plus the ICE credentials an attach
// needs, fetched in the same parallel warm-up. It returns the warm-up so the
// caller can use (or discard) those credentials.
func (a *app) resolveMachinesWarm(ctx context.Context, dir string, names []string, idn *client.Identity, turnURL string, stun []string) ([]client.Machine, *client.Prewarm, error) {
	local, err := client.ListMachines(dir)
	if err != nil {
		return nil, nil, err
	}
	warm := a.prewarm(ctx, dir, idn, turnURL, stun)
	discovered := warm.Discovered
	fetchErr := warm.RegistryErr
	if idn.HasRootedIdentity() {
		_ = client.NotifyNewDevices(a.errOut, dir, discovered)
	}
	revocations, err := client.ListRevocations(dir)
	if err != nil {
		return nil, nil, err
	}
	local = client.FilterRevoked(local, idn.OwnerID, revocations)
	resolved, missing := resolveNames(local, client.FilterRevoked(discovered, idn.OwnerID, revocations), names)
	if missing != "" && warm.RegistryIsCached() {
		// A cached registry can be a beat behind a machine paired seconds ago on
		// another device. Before calling a name unknown, ask the relay.
		live, refreshErr := warm.RefreshRegistry(ctx)
		if refreshErr != nil {
			fetchErr = refreshErr
		} else {
			fetchErr = nil
			_ = client.NotifyNewDevices(a.errOut, dir, live)
			resolved, missing = resolveNames(local, client.FilterRevoked(live, idn.OwnerID, revocations), names)
		}
	}
	if missing != "" {
		if fetchErr != nil {
			return nil, nil, fmt.Errorf("unknown machine %q — it is not paired locally, and the relay was unreachable so your encrypted registry could not be checked; get back online and retry (cause: %v)", missing, fetchErr)
		}
		return nil, nil, fmt.Errorf("unknown machine %q — it is neither paired locally nor online in your encrypted registry", missing)
	}
	return resolved, warm, nil
}

// defaultAttachTarget picks what a bare `mir attach` means: the last-used
// machine when it still exists, else the only machine there is. "" (with a nil
// error) means "no obvious default — open the overview".
func (a *app) defaultAttachTarget(dir string) (string, error) {
	idn, err := a.identity(dir)
	if err != nil {
		return "", err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return "", err
	}
	local, err := client.ListMachines(dir)
	if err != nil {
		return "", err
	}
	warm := a.prewarm(context.Background(), dir, idn, "", nil)
	revocations, err := client.ListRevocations(dir)
	if err != nil {
		return "", err
	}
	merged := client.FilterRevoked(client.MergeMachines(local, warm.Discovered), idn.OwnerID, revocations)
	if len(merged) == 0 {
		return "", fmt.Errorf("no machines yet — run `%s up` on the machine you want to reach; its first run shows a pairing QR", a.binary)
	}
	if m, ok := pickDefaultMachine(client.LastUsed(dir), merged); ok {
		return m.Name, nil
	}
	return "", nil
}

// resolveNames maps names onto machines, returning the first name that resolved
// nowhere ("" when every name landed).
func resolveNames(local, discovered []client.Machine, names []string) ([]client.Machine, string) {
	resolved := make([]client.Machine, 0, len(names))
	for _, name := range names {
		m, ok, _ := client.ResolveMachine(local, discovered, name)
		if !ok {
			return nil, name
		}
		resolved = append(resolved, m)
	}
	return resolved, ""
}

// localRelayFor guesses which relay will mint this attach's TURN credentials, so
// the fetch can start alongside discovery instead of waiting for it: the pinned
// machine's own relay when we know it, else the default. A guess that turns out
// wrong costs one extra round trip (cmdAttach refetches), never a wrong path.
func localRelayFor(dir, name string) string {
	if m, err := client.GetMachine(dir, name); err == nil && strings.TrimSpace(m.SignalURL) != "" {
		return m.SignalURL
	}
	return defaults.SignalURL()
}

// sameRelay compares two relay base URLs the way the cache scopes them.
func sameRelay(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

func (a *app) cmdMachine(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s machine rename <name> <new-name> | %s machine revoke <name> [--yes]", a.binary, a.binary)
	}
	switch args[0] {
	case "revoke":
		return a.cmdMachineRevoke(args[1:])
	case "rename":
		return a.cmdMachineRename(args[1:])
	default:
		return fmt.Errorf("unknown machine subcommand %q", args[0])
	}
}

// cmdMachineRename renames a machine everywhere: locally at once, then — the
// agent can't seal registry records itself — it re-seals the discovery record
// under the owner root and delivers it to the machine over an authenticated
// session. The machine persists the name, republishes the record on its live
// relay registration, and your other devices converge on the registry's newer
// ts (see client.MergeMachines).
func (a *app) cmdMachineRename(args []string) error {
	fs := flag.NewFlagSet("machine rename", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	ice := iceFlags(fs)
	rest := parseArgs(fs, args)
	if len(rest) != 2 {
		return fmt.Errorf("usage: %s machine rename <name> <new-name>", a.binary)
	}
	name, newName := rest[0], rest[1]
	if !agent.ValidMachineName(newName) {
		return fmt.Errorf("invalid machine name %q: 1-64 characters, no control characters or surrounding spaces", newName)
	}
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	machines, err := a.resolveMachines(context.Background(), *dir, []string{name}, idn)
	if err != nil {
		return err
	}
	m := machines[0]
	if newName == m.Name {
		fmt.Fprintf(a.out, "machine is already named %q\n", newName)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.renameResolved(ctx, *dir, idn, m, newName, ice()); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ renamed %q → %q — the new name reaches your other devices via the encrypted registry\n", name, newName)
	return nil
}

// renameResolved is the rename core shared by `mir machine rename` and the
// overview: validate, re-seal under the owner root, rename locally first, then
// deliver the re-sealed record to the machine over an authenticated session.
func (a *app) renameResolved(ctx context.Context, dir string, idn *client.Identity, m client.Machine, newName string, ice []peer.ICEServer) error {
	if !agent.ValidMachineName(newName) {
		return fmt.Errorf("invalid machine name %q: 1-64 characters, no control characters or surrounding spaces", newName)
	}
	blob, ts, err := client.SealRegistryMachine(idn, client.Machine{
		Name: newName, MachineID: m.MachineID, HostPubHex: m.HostPubHex, SignalURL: m.SignalURL,
	})
	if err != nil {
		return err
	}
	// Local first: this device shows the new name immediately (and keeps
	// winning the merge until the machine republishes the re-sealed record).
	if err := client.RenameLocalMachine(dir, m, newName, ts); err != nil {
		return err
	}
	mc, sess, cleanup, err := client.Attach(ctx, m, idn, ice)
	if err != nil {
		return fmt.Errorf("renamed locally, but %q is unreachable (%v) — your other devices keep the old name; re-run when it is back online", m.Name, err)
	}
	defer cleanup()
	if err := client.RenameOverSession(ctx, mc, sess, newName, blob, 8*time.Second); err != nil {
		return fmt.Errorf("renamed locally, but delivery was not confirmed (%v) — the machine may run an older agent; update it and re-run", err)
	}
	return nil
}

// machineRevokeIsTTY is a seam for tests; retirement asks before acting only
// when a person is there to answer.
var machineRevokeIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// confirmRetire spells out what retiring a machine does and asks. Default is
// no — this is the client's one deliberately heavy trust decision.
func (a *app) confirmRetire(name string) bool {
	if a.in == nil {
		return false
	}
	fmt.Fprintf(a.out, "Retiring %q:\n", name)
	fmt.Fprintln(a.out, "  - it disappears from your machine list on every device")
	fmt.Fprintln(a.out, "  - your identity can no longer reach it")
	fmt.Fprintln(a.out, "  - the machine keeps running; tmux sessions on it are untouched")
	fmt.Fprintln(a.out, "  - to use it again: run `mir up` on it and pair fresh")
	fmt.Fprintf(a.out, "Retire %q? [y/N] ", name)
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func (a *app) cmdMachineRevoke(args []string) error {
	fs := flag.NewFlagSet("machine revoke", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	yes := fs.Bool("yes", false, "skip the interactive confirmation (scripts)")
	// Accepts the human-friendly documented form `revoke box --yes` as well as
	// Go flag's native `revoke --yes box` ordering (parseArgs).
	rest := parseArgs(fs, args)
	if len(rest) != 1 {
		return fmt.Errorf("usage: %s machine revoke <name> [--yes]", a.binary)
	}
	name := rest[0]
	// Consent before any identity or network work. Interactive runs get the
	// plain-words prompt; scripted runs must state --yes (fail closed).
	if !*yes {
		if !machineRevokeIsTTY() {
			return fmt.Errorf("retiring a machine is permanent for this owner id; re-run with --yes, or run from a terminal to confirm interactively")
		}
		if !a.confirmRetire(name) {
			fmt.Fprintln(a.out, "nothing changed")
			return nil
		}
	}
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	local, err := client.ListMachines(*dir)
	if err != nil {
		return err
	}
	var discovered []client.Machine
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	discovered, _ = client.FetchRegistry(ctx, nil, defaults.SignalURL(), idn)
	machine, ok, _ := client.ResolveMachine(local, discovered, name)
	if !ok {
		return fmt.Errorf("unknown machine %q", name)
	}
	return a.retireResolved(ctx, a.out, *dir, idn, machine)
}

// retireResolved is the retirement core shared by `mir machine revoke` and the
// overview: sign the revocation, record it locally first, then publish it to
// the relays. Progress lines go to w (the overview passes io.Discard and shows
// its own one-line status instead).
func (a *app) retireResolved(ctx context.Context, w io.Writer, dir string, idn *client.Identity, machine client.Machine) error {
	signer, err := idn.Signer()
	if err != nil {
		return err
	}
	record, err := signer.SignRevocation(machine.MachineID, time.Now())
	if err != nil {
		return err
	}
	// Local-first is intentional: even when every relay is unavailable, this
	// client must stop trusting the target immediately.
	if err := client.RecordRevocation(dir, *record); err != nil {
		return err
	}
	fmt.Fprintf(w, "revoked %q locally (%s)\n", machine.Name, machine.MachineID)

	relays := []string{machine.SignalURL, defaults.SignalURL()}
	seen := map[string]bool{}
	var publishErrors []string
	for _, relay := range relays {
		relay = strings.TrimRight(strings.TrimSpace(relay), "/")
		if relay == "" || seen[relay] {
			continue
		}
		seen[relay] = true
		if err := client.PostRevocation(ctx, nil, relay, *record); err != nil {
			publishErrors = append(publishErrors, relay+": "+err.Error())
			continue
		}
		fmt.Fprintf(w, "published signed revocation to %s\n", relay)
	}
	if len(publishErrors) > 0 {
		return fmt.Errorf("machine is blocked locally, but relay publication failed (%s); retry the command when online", strings.Join(publishErrors, "; "))
	}
	fmt.Fprintf(w, "\n✓ retired %q — your identity can no longer reach it, on any device.\n", machine.Name)
	fmt.Fprintln(w, "The machine itself keeps running; nothing on it was touched.")
	fmt.Fprintln(w, "To use it again: run `mir up` on it and pair fresh.")
	return nil
}

func (a *app) cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	prefixFlag := fs.String("prefix", "ctrl-o", "multiplexer switch key (e.g. ctrl-o, ctrl-a, ctrl-space)")
	relayOnly := fs.Bool("relay-only", false, "deprecated: no effect (one connection now carries direct and relayed)")
	ice := iceFlags(fs)
	names := parseArgs(fs, args)
	if *relayOnly {
		fmt.Fprintln(a.errOut, "note: --relay-only no longer does anything and will go away — LAN-direct now rides the same connection (direct when possible, relayed when not)")
	}
	if len(names) == 0 {
		// A bare `mir attach` on a terminal means "continue": the last-used
		// machine, else the only one there is, else the overview. Scripts
		// (no TTY) keep the explicit usage error.
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			return fmt.Errorf("usage: %s attach <machine> [machine...]", a.binary)
		}
		name, err := a.defaultAttachTarget(*dir)
		if err != nil {
			return err
		}
		if name == "" {
			return a.cmdOverview()
		}
		fmt.Fprintf(a.errOut, "[%s] attaching %s — run `%s` alone to pick from your machines\n", a.binary, name, a.binary)
		names = []string{name}
	}
	prefix, prefixLabel, err := parsePrefix(*prefixFlag)
	if err != nil {
		return err
	}
	servers := ice()
	idn, err := a.identity(*dir)
	if err != nil {
		return err
	}
	if err := a.requireRootedIdentity(idn); err != nil {
		return err
	}
	// attach is long-lived, so the backgrounded refresh has time to land for the
	// next run; surface any cached newer version now (non-blocking).
	updateClient(a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Credentials are minted per relay, and which relay this attach uses is only
	// known after discovery. Guess from the local pin so the fetch runs beside
	// discovery instead of after it; refetch only if the guess was wrong.
	turnURL := ""
	if !iceHasTURN(servers) {
		turnURL = localRelayFor(*dir, names[0])
	}
	resolved, warm, err := a.resolveMachinesWarm(ctx, *dir, names, idn, turnURL, iceSTUNURLs(servers))
	if err != nil {
		return err
	}
	// A share is checked against its own clock before dialing: an expired grant
	// would only earn the agent's silent refusal, which reads as "offline".
	for _, m := range resolved {
		if m.Owner == "" {
			continue
		}
		g := client.GuestGrantFor(*dir, m.MachineID)
		if g == nil || g.ValidAt(time.Now()) != nil {
			return fmt.Errorf("your share of %q has ended — ask the owner for a new invite", m.Name)
		}
	}
	iceList := servers
	if len(resolved) > 0 && !iceHasTURN(servers) {
		if warm.ICEErr == nil && sameRelay(warm.ICEFrom, resolved[0].SignalURL) {
			iceList = warm.ICE
		} else if got, err := client.ResolveICE(ctx, resolved[0].SignalURL, iceSTUNURLs(servers)); err == nil {
			iceList = got
		}
	}
	if len(resolved) == 1 {
		m := resolved[0]
		// One clear line per state change; \r\n because the terminal may still be
		// settling out of raw mode. The reconnected line carries the measured
		// outage — the number the NAT-matrix work (P2) reads from real runs.
		notify := client.ReconnectNotify{
			OnReconnecting: func(attempt int) {
				if attempt == 1 {
					fmt.Fprintf(a.errOut, "\r\n[mir] connection lost — reconnecting…\r\n")
				} else {
					fmt.Fprintf(a.errOut, "[mir] still reconnecting (attempt %d)…\r\n", attempt)
				}
			},
			OnResumed: func(outage time.Duration) {
				fmt.Fprintf(a.errOut, "[mir] reconnected in %.1fs\r\n", outage.Seconds())
			},
			OnGaveUp: func(failures int, lastErr error) {
				fmt.Fprintf(a.errOut, "\r\n[mir] is the machine up? Check `%s list`, then rerun `%s attach %s`.\r\n", a.binary, a.binary, m.Name)
			},
		}
		var once sync.Once
		err := client.ReconnectLoopWith(ctx, client.ReconnectPolicy{Notify: notify}, func(ctx context.Context) (peer.MsgConn, *noise.Session, func(), error) {
			return client.Attach(ctx, m, idn, iceList)
		}, func(ctx context.Context, mc peer.MsgConn, sess *noise.Session) error {
			once.Do(func() { client.SaveLastUsed(*dir, m.Name) })
			return client.RunInteractive(ctx, mc, sess, m.Name)
		})
		if err != nil && ctx.Err() == nil && !isCleanDetach(err) {
			return humanAttachErr(a.binary, m.Name, err)
		}
		return nil
	}

	sessions, cleanup, err := client.AttachAll(ctx, resolved, idn, iceList)
	if err != nil {
		return humanAttachErr(a.binary, strings.Join(names, ", "), err)
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions, prefix, prefixLabel); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

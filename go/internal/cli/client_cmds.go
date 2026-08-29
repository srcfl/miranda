package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := selfupdate.New(repoSlug, a.binary)
	rel, err := c.Latest()
	if err != nil {
		return err
	}
	if !selfupdate.IsNewer(version.Version, rel.Tag) {
		fmt.Fprintf(a.out, "already up to date (%s)\n", version.Version)
		return nil
	}
	fmt.Fprintf(a.out, "updating %s %s → %s …\n", a.binary, version.Version, rel.Tag)
	if err := c.Apply(rel, exe); err != nil {
		return err
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
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: mir run <machine> <command...>")
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

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, ice(), false)
	if err != nil {
		return err
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
	selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)
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
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		a.syncRevocations(ctx, *dir, idn, []string{defaults.SignalURL()})
		disc, _ := client.FetchRegistry(ctx, nil, defaults.SignalURL(), idn)
		cancel()
		discovered = disc
		for _, m := range disc {
			discoveredID[m.MachineID] = true
		}
		// One-line "new device joined" notice on stderr, so stdout stays script-clean.
		_ = client.NotifyNewDevices(a.errOut, *dir, disc)
	}

	revocations, err := client.ListRevocations(*dir)
	if err != nil {
		return err
	}
	merged := client.FilterRevoked(client.MergeMachines(local, discovered), idn.OwnerID, revocations)
	if len(merged) == 0 {
		fmt.Fprintln(a.out, "no machines yet — run `mir pair` on a target, then scan its QR or pair with its code")
		return nil
	}
	for _, m := range merged {
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

// resolveMachines resolves every name against the local pin set and the owner's
// encrypted live registry in one fetch. This gives single attach, multi-attach,
// and non-interactive run identical zero-config discovery behavior.
func (a *app) resolveMachines(ctx context.Context, dir string, names []string, idn *client.Identity) ([]client.Machine, error) {
	local, err := client.ListMachines(dir)
	if err != nil {
		return nil, err
	}
	var discovered []client.Machine
	if idn.HasRootedIdentity() {
		fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		a.syncRevocations(fctx, dir, idn, []string{defaults.SignalURL()})
		discovered, _ = client.FetchRegistry(fctx, nil, defaults.SignalURL(), idn)
		cancel()
		_ = client.NotifyNewDevices(a.errOut, dir, discovered)
	}
	revocations, err := client.ListRevocations(dir)
	if err != nil {
		return nil, err
	}
	local = client.FilterRevoked(local, idn.OwnerID, revocations)
	discovered = client.FilterRevoked(discovered, idn.OwnerID, revocations)
	resolved := make([]client.Machine, 0, len(names))
	for _, name := range names {
		m, ok, _ := client.ResolveMachine(local, discovered, name)
		if !ok {
			return nil, fmt.Errorf("unknown machine %q — it is neither paired locally nor online in your encrypted registry", name)
		}
		resolved = append(resolved, m)
	}
	return resolved, nil
}

// syncRevocations best-effort gossips signed tombstones from each relay into the
// fail-closed local store. Relay availability is not authority: already-cached
// records are always enforced, and every newly fetched record is owner-verified.
func (a *app) syncRevocations(ctx context.Context, dir string, idn *client.Identity, relays []string) {
	seen := map[string]bool{}
	for _, relay := range relays {
		relay = strings.TrimRight(strings.TrimSpace(relay), "/")
		if relay == "" || seen[relay] {
			continue
		}
		seen[relay] = true
		records, err := client.FetchRevocations(ctx, nil, relay, idn.OwnerID)
		if err != nil {
			continue
		}
		for _, record := range records {
			if err := client.RecordRevocation(dir, record); err != nil {
				fmt.Fprintf(a.errOut, "warning: could not cache signed revocation: %v\n", err)
				return
			}
		}
	}
}

func (a *app) cmdMachine(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mir machine revoke <name> --yes")
	}
	switch args[0] {
	case "revoke":
		return a.cmdMachineRevoke(args[1:])
	default:
		return fmt.Errorf("unknown machine subcommand %q", args[0])
	}
}

func (a *app) cmdMachineRevoke(args []string) error {
	fs := flag.NewFlagSet("machine revoke", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	yes := fs.Bool("yes", false, "confirm permanent machine revocation")
	// Accept the human-friendly documented form `revoke box --yes` as well as
	// Go flag's native `revoke --yes box` ordering.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	_ = fs.Parse(args)
	if name == "" {
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
	} else if len(fs.Args()) != 0 {
		return fmt.Errorf("usage: mir machine revoke <name> --yes")
	}
	if name == "" || len(fs.Args()) > 1 {
		return fmt.Errorf("usage: mir machine revoke <name> --yes")
	}
	if !*yes {
		return fmt.Errorf("machine revocation is permanent for this owner id; re-run with --yes")
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
	if err := client.RecordRevocation(*dir, *record); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "revoked %q locally (%s)\n", machine.Name, machine.MachineID)

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
		fmt.Fprintf(a.out, "published signed revocation to %s\n", relay)
	}
	if len(publishErrors) > 0 {
		return fmt.Errorf("machine is blocked locally, but relay publication failed (%s); retry the command when online", strings.Join(publishErrors, "; "))
	}
	return nil
}

func (a *app) cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultClientDir(), "client state directory")
	prefixFlag := fs.String("prefix", "ctrl-o", "multiplexer switch key (e.g. ctrl-o, ctrl-a, ctrl-space)")
	relayOnly := fs.Bool("relay-only", false, "skip LAN-direct discovery; use the relay")
	ice := iceFlags(fs)
	_ = fs.Parse(args)
	names := fs.Args()
	if len(names) == 0 {
		return fmt.Errorf("usage: mir attach <machine> [machine...]")
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
	selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	resolved, err := a.resolveMachines(ctx, *dir, names, idn)
	if err != nil {
		return err
	}
	iceList := servers
	if len(resolved) > 0 && !iceHasTURN(servers) {
		if got, err := client.ResolveICE(ctx, resolved[0].SignalURL, iceSTUNURLs(servers)); err == nil {
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
		err := client.ReconnectLoopWith(ctx, client.ReconnectPolicy{Notify: notify}, func(ctx context.Context) (peer.MsgConn, *noise.Session, func(), error) {
			return client.Attach(ctx, m, idn, iceList, *relayOnly)
		}, func(ctx context.Context, mc peer.MsgConn, sess *noise.Session) error {
			return client.RunInteractive(ctx, mc, sess, m.Name)
		})
		if err != nil && ctx.Err() == nil && !isCleanDetach(err) {
			return err
		}
		return nil
	}

	sessions, cleanup, err := client.AttachAll(ctx, resolved, idn, iceList, *relayOnly)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions, prefix, prefixLabel); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

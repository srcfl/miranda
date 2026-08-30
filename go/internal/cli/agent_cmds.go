package cli

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/selfupdate"
	"github.com/srcful/terminal-relay/go/internal/version"
)

func (a *app) cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", defaultAgentDir(), "agent state directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	_ = fs.Parse(args)
	if err := ensureAgentOnlyDir(*dir); err != nil {
		return err
	}

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "enrolled %q\n  machine_id: %s\n  host_pub:   %s\n  signal:     %s\n",
		cfg.MachineName, cfg.MachineID, cfg.HostPubHex, cfg.SignalURL)
	fmt.Fprintln(a.out, "\nNext: run `mir up` — the first run shows a pairing QR. For local dev:")
	fmt.Fprintf(a.out, "  mir pair-dev --owner-id <base58 owner id from mir identity>\n")
	if !agent.TmuxInstalled() {
		fmt.Fprintln(a.out, "\nwarning: tmux is not installed (needed for persistent sessions): brew install tmux")
	}
	return nil
}

func (a *app) cmdPairDev(args []string) error {
	fs := flag.NewFlagSet("pair-dev", flag.ExitOnError)
	dir := fs.String("dir", defaultAgentDir(), "agent state directory")
	ownerID := fs.String("owner-id", "", "Miranda owner id (base58 from `mir identity`)")
	ownerPub := fs.String("owner-pub", "", "deprecated alias for --owner-id (must be the owner id, not X25519 hex)")
	_ = fs.Parse(args)
	if err := ensureAgentOnlyDir(*dir); err != nil {
		return err
	}
	id := strings.TrimSpace(*ownerID)
	if id == "" {
		id = strings.TrimSpace(*ownerPub)
	}
	if id == "" {
		return fmt.Errorf("--owner-id is required (base58 from `mir identity show`)")
	}
	if looksLikeX25519Hex(id) {
		return fmt.Errorf("pair-dev takes the Miranda owner id (base58), not the X25519 hex; run `mir identity show`")
	}
	if err := agent.PinOwner(*dir, id); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "pinned owner %s\n", id)
	return nil
}

func (a *app) cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	dir := fs.String("dir", defaultAgentDir(), "agent state directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL the first-run pairing QR opens")
	shell := fs.String("shell", "tmux:new:-A:-s:main", "launch command, ':'-separated (the default gives each viewer its own tmux view; a custom command attaches as-is)")
	ice := iceFlags(fs)
	autoUpdate := fs.Bool("auto-update", os.Getenv("MIR_AUTO_UPDATE") == "1", "opt-in: automatically self-update when idle")
	noLAN := fs.Bool("no-lan", false, "deprecated: no effect (one connection now carries direct and relayed)")
	allowRoot := fs.Bool("allow-root", false, "unsafe override: allow the terminal agent to run as root")
	noPair := fs.Bool("no-pair", false, "first run: do not offer inline pairing; fail if no owner is paired")
	confirmSAS := fs.String("confirm-sas", "", "first-run pairing, non-interactive: the expected safety number")
	yes := fs.Bool("yes", false, "first-run pairing, non-interactive: commit without comparing the safety number")
	_ = fs.Parse(args)
	if *noLAN {
		fmt.Fprintln(a.errOut, "note: --no-lan no longer does anything and will go away — LAN-direct now rides the same connection (direct when possible, relayed when not)")
	}
	if err := ensureAgentOnlyDir(*dir); err != nil {
		return err
	}
	if os.Geteuid() == 0 && !*allowRoot {
		return fmt.Errorf("refusing to expose a root shell; run mir as the target user (or pass --allow-root only in an isolated environment)")
	}

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		return err
	}
	launch := strings.Split(*shell, ":")
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	// tmux bootstrap runs BEFORE pairing: on a fresh machine both need the
	// user's attention, and asking everything up front (tmux, then the pairing
	// QR) reads as one flow instead of two interruptions.
	launch, err = newTmuxBootstrap(isTTY, a.in).ensure(a.out, a.errOut, launch)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// First run: nothing can attach before an owner is paired (the runtime would
	// refuse to start), so pair right here — QR on the same screen the user is
	// already looking at — then serve. `mir pair` stays for adding owners later.
	if len(cfg.PairedOwners) == 0 && !*noPair {
		gate := sasGate{
			confirmSAS: *confirmSAS,
			skip:       *yes,
			isTTY:      isTTY,
			in:         os.Stdin,
		}
		if err := a.pairOnFirstRun(ctx, *dir, *name, *signalURL, *webURL, gate); err != nil {
			if ctx.Err() != nil {
				return nil // interrupted while waiting to pair: a clean shutdown
			}
			return err
		}
		if cfg, err = agent.LoadOrInit(*dir, *name, *signalURL); err != nil {
			return err
		}
	}

	rt := agent.NewRuntime(cfg, launch, ice())
	// Structured, timestamped agent log. RFC3339-ish date+time in UTC plus the
	// binary prefix turns a bare "owner … disconnected" line into something you
	// can correlate against relay logs and tell a flap (low uptime) from a normal
	// idle reconnect at a glance. Logger.Printf appends the newline.
	rlog := log.New(a.errOut, a.binary+": ", log.LstdFlags|log.LUTC)
	rt.Logf = rlog.Printf
	fmt.Fprintf(a.out, "%s up: machine %s, signaling %s\n", a.binary, cfg.MachineID, cfg.SignalURL)
	// Non-blocking update notice (cache-only display; refresh in background while serving).
	updateClient(a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)
	if *autoUpdate {
		go a.autoUpdateLoop(ctx, rt)
	}
	if err := rt.Up(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// autoUpdateLoop checks for a newer release every 12h and applies it only when no
// owner session is active, then re-execs into the new binary (preserving PID/FDs
// so a systemd/supervisor wrapper survives). Opt-in via --auto-update / MIR_AUTO_UPDATE.
func (a *app) autoUpdateLoop(ctx context.Context, rt *agent.Runtime) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := updateClient(a.binary)
	check := func() {
		if rt.ActiveSessions() > 0 {
			return // a client is connected — defer the swap until idle
		}
		rel, err := c.Latest()
		if err != nil || !selfupdate.IsNewer(version.Version, rel.Tag) {
			return
		}
		if err := c.Apply(rel, exe); err != nil {
			fmt.Fprintf(a.errOut, "%s: auto-update failed: %v\n", a.binary, err)
			return
		}
		// TOCTOU guard: Apply did two HTTP fetches, during which an owner could
		// have attached. ReExec (syscall.Exec) is immediate and would kill that
		// session mid-stream. Re-check idleness right before the swap; if a session
		// raced in, abort and try again on the next tick (the freshly-written
		// binary stays on disk and is picked up then).
		if rt.ActiveSessions() > 0 {
			fmt.Fprintf(a.errOut, "%s: session attached during update; deferring restart\n", a.binary)
			return
		}
		fmt.Fprintf(a.errOut, "%s: updated → %s, restarting\n", a.binary, rel.Tag)
		_ = selfupdate.ReExec(exe, os.Args, os.Environ())
	}
	check() // once at startup (serving has begun; gated on idle)
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

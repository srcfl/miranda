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

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/selfupdate"
	"github.com/srcful/terminal-relay/go/internal/version"
)

func (a *app) cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "enrolled %q\n  machine_id: %s\n  host_pub:   %s\n  signal:     %s\n",
		cfg.MachineName, cfg.MachineID, cfg.HostPubHex, cfg.SignalURL)
	fmt.Fprintln(a.out, "\nNext: pair an owner. For local dev:")
	fmt.Fprintf(a.out, "  mir pair-dev --owner-pub <hex>\n")
	if !agent.TmuxInstalled() {
		fmt.Fprintln(a.out, "\nwarning: tmux is not installed (needed for persistent sessions): brew install tmux")
	}
	return nil
}

func (a *app) cmdPairDev(args []string) error {
	fs := flag.NewFlagSet("pair-dev", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	ownerPub := fs.String("owner-pub", "", "owner X25519 public key (hex) to trust")
	_ = fs.Parse(args)
	if *ownerPub == "" {
		return fmt.Errorf("--owner-pub is required")
	}
	if err := agent.PinOwner(*dir, strings.ToLower(*ownerPub)); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "pinned owner %s\n", *ownerPub)
	return nil
}

func (a *app) cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	shell := fs.String("shell", "tmux:new:-A:-s:main", "launch command, ':'-separated")
	ice := iceFlags(fs)
	autoUpdate := fs.Bool("auto-update", os.Getenv("MIR_AUTO_UPDATE") == "1", "opt-in: automatically self-update when idle")
	noLAN := fs.Bool("no-lan", false, "disable LAN-direct (no QUIC listener, no mDNS advertise); serve the relay only")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		return err
	}
	launch := strings.Split(*shell, ":")
	if launch[0] == "tmux" && !agent.TmuxInstalled() {
		return fmt.Errorf("tmux not installed (brew install tmux), or pass --shell sh")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt := agent.NewRuntime(cfg, launch, ice())
	rt.DisableLAN = *noLAN
	// Wallet-rooted machines auto-serve their own wallet (no pairing for your own
	// devices) and publish an encrypted registry record. Legacy (wallet-less) mir
	// up is unchanged: it serves PairedOwners and publishes nothing.
	if err := a.applyWalletToUp(*dir, rt); err != nil {
		return err
	}
	// Structured, timestamped agent log. RFC3339-ish date+time in UTC plus the
	// binary prefix turns a bare "owner … disconnected" line into something you
	// can correlate against relay logs and tell a flap (low uptime) from a normal
	// idle reconnect at a glance. Logger.Printf appends the newline.
	rlog := log.New(a.errOut, a.binary+": ", log.LstdFlags|log.LUTC)
	rt.Logf = rlog.Printf
	fmt.Fprintf(a.out, "%s up: machine %s, signaling %s\n", a.binary, cfg.MachineID, cfg.SignalURL)
	// Non-blocking update notice (cache-only display; refresh in background while serving).
	selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)
	if *autoUpdate {
		go a.autoUpdateLoop(ctx, rt)
	}
	if err := rt.Up(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// applyWalletToUp wires this machine's wallet into the serving Runtime. On a
// wallet-rooted identity it auto-pins the machine's OWN wallet as a served owner
// (so your own devices attach with no SAS/pairing — B1.4 bindings) and hands the
// wallet secret + address to the Runtime so it can seal + publish its encrypted
// registry record on the live registration. A wallet-less (legacy) identity is a
// no-op: `mir up` keeps today's behavior (serve PairedOwners, publish nothing).
// PinOwner writes config.json's PairedOwners (the agent hot-reloads owners; pinning
// before Up() puts it in the initial set). Any pin failure aborts so we never serve
// in a half-configured state.
func (a *app) applyWalletToUp(dir string, rt *agent.Runtime) error {
	idn, err := a.identity(dir)
	if err != nil || !idn.HasWallet() {
		return nil // legacy / no wallet: unchanged behavior
	}
	if err := agent.PinOwner(dir, idn.WalletAddress); err != nil {
		return err
	}
	rt.WalletSecret = idn.Secret()
	rt.WalletAddress = idn.WalletAddress
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
	c := selfupdate.New(repoSlug, a.binary)
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

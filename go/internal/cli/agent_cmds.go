package cli

import (
	"context"
	"flag"
	"fmt"
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
	rt.Logf = func(f string, args ...any) { fmt.Fprintf(a.errOut, a.binary+": "+f+"\n", args...) }
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

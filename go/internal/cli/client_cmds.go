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
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/selfupdate"
	"github.com/srcful/terminal-relay/go/internal/version"
)

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
	dir := fs.String("dir", defaultDir(), "config directory")
	ice := iceFlags(fs)
	window := fs.Duration("window", 3*time.Second, "how long to stream output before exiting")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: mir run <machine> <command...>")
	}
	name := rest[0]
	cmd := strings.Join(rest[1:], " ")

	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	m, err := client.GetMachine(*dir, name)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, ice())
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
	dir := fs.String("dir", defaultDir(), "config directory")
	wallet := fs.Bool("wallet", false, "re-key a legacy identity into a prf-rooted wallet identity (changes owner_id; re-pair needed)")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	if *wallet && !id.HasWallet() {
		if id, err = client.Rekey(*dir); err != nil {
			return err
		}
		fmt.Fprintln(a.errOut, "re-keyed to a prf-rooted wallet identity — owner_id changed, re-pair your machines")
	}
	fmt.Fprintf(a.out, "owner public key:\n  %s\n\nPin it on each machine:\n  mir pair-dev --owner-pub %s\n", id.OwnerPubHex, id.OwnerPubHex)
	if id.HasWallet() {
		fmt.Fprintf(a.out, "\nwallet address:\n  %s\n", id.WalletAddress)
	}
	return nil
}

func (a *app) cmdAddMachine(args []string) error {
	fs := flag.NewFlagSet("add-machine", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
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
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	// Cheap, non-blocking update notice (cache-only display; refresh in background).
	selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)
	list, err := client.ListMachines(*dir)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(a.out, "no machines yet — add one with `mir add-machine`")
		return nil
	}
	for _, m := range list {
		fmt.Fprintf(a.out, "%-16s %s  %s\n", m.Name, m.MachineID, m.SignalURL)
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

func (a *app) cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	prefixFlag := fs.String("prefix", "ctrl-o", "multiplexer switch key (e.g. ctrl-o, ctrl-a, ctrl-space)")
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
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	// attach is long-lived, so the backgrounded refresh has time to land for the
	// next run; surface any cached newer version now (non-blocking).
	selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(names) == 1 {
		m, err := client.GetMachine(*dir, names[0])
		if err != nil {
			return err
		}
		mc, sess, cleanup, err := client.Attach(ctx, *m, idn, servers)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := client.RunInteractive(ctx, mc, sess, m.Name); err != nil && ctx.Err() == nil && !isCleanDetach(err) {
			return err
		}
		return nil
	}

	sessions, cleanup, err := client.AttachAll(ctx, *dir, names, idn, servers)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions, prefix, prefixLabel); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

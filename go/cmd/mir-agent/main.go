// go/cmd/mir-agent/main.go
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/sas"
	"github.com/srcful/terminal-relay/go/internal/selfupdate"
	"github.com/srcful/terminal-relay/go/internal/version"
)

func defaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terminal-relay")
}

const repoSlug = "srcfl/miranda"

func updateCachePath(dir string) string {
	return filepath.Join(dir, "update-check.json")
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "--version", "-v", "version":
		fmt.Println("mir-agent", version.String())
		return
	case "enroll":
		cmdEnroll(os.Args[2:])
	case "pair-dev":
		cmdPairDev(os.Args[2:])
	case "pair":
		cmdPair(os.Args[2:])
	case "up":
		cmdUp(os.Args[2:])
	case "self-update":
		cmdSelfUpdate(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mir-agent <enroll|pair-dev|pair|up|self-update|--version> [flags]")
	os.Exit(2)
}

// cmdSelfUpdate replaces the running mir-agent binary with the latest GitHub
// Release (verified by SHA256) when a newer version exists.
func cmdSelfUpdate(args []string) {
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := selfupdate.New(repoSlug, "mir-agent")
	rel, err := c.Latest()
	if err != nil {
		fatal(err)
	}
	if !selfupdate.IsNewer(version.Version, rel.Tag) {
		fmt.Printf("already up to date (%s)\n", version.Version)
		return
	}
	fmt.Printf("updating mir-agent %s → %s …\n", version.Version, rel.Tag)
	if err := c.Apply(rel, exe); err != nil {
		fatal(err)
	}
	fmt.Printf("updated mir-agent → %s\n", rel.Tag)
}

func cmdEnroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("enrolled %q\n  machine_id: %s\n  host_pub:   %s\n  signal:     %s\n",
		cfg.MachineName, cfg.MachineID, cfg.HostPubHex, cfg.SignalURL)
	fmt.Println("\nNext: pair an owner. For local dev:")
	fmt.Printf("  mir-agent pair-dev --owner-pub <hex>\n")
	if !agent.TmuxInstalled() {
		fmt.Println("\nwarning: tmux is not installed (needed for persistent sessions): brew install tmux")
	}
}

func cmdPairDev(args []string) {
	fs := flag.NewFlagSet("pair-dev", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	ownerPub := fs.String("owner-pub", "", "owner X25519 public key (hex) to trust")
	_ = fs.Parse(args)
	if *ownerPub == "" {
		fatal(fmt.Errorf("--owner-pub is required"))
	}
	if err := agent.PinOwner(*dir, strings.ToLower(*ownerPub)); err != nil {
		fatal(err)
	}
	fmt.Printf("pinned owner %s\n", *ownerPub)
}

func cmdPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL (the QR opens this)")
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}

	token := pairing.NewToken()
	code := pairing.EncodeCode(*signalURL, token)
	pairURL := strings.TrimRight(*webURL, "/") + "/#" + code

	fmt.Println("Pair this machine:")
	fmt.Print("\n  📱 Scan with your phone's camera — it opens the app ready to pair:\n\n")
	qrterminal.GenerateHalfBlock(pairURL, qrterminal.L, os.Stdout)
	fmt.Printf("\n  …or open: %s\n", pairURL)
	fmt.Printf("  …or on the CLI:  mir pair %s\n", code)
	fmt.Printf("\nwaiting for pairing (5 min)…\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, *signalURL, pairing.RoomID(token))
	if err != nil {
		fatal(err)
	}
	defer closeConn()

	info := pairing.AgentInfo{HostPubHex: cfg.HostPubHex, MachineID: cfg.MachineID, Name: cfg.MachineName}
	ownerPub, binding, err := pairing.RunResponder(ctx, mc, token, info)
	if err != nil {
		fatal(err)
	}
	ownerHex := hex.EncodeToString(ownerPub)
	if err := agent.PinOwner(*dir, ownerHex); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ paired — trusting owner %s…\n", ownerHex[:16])
	fmt.Printf("  safety number: %s  (must match the client's)\n", sas.FromBinding(binding))
}

func cmdUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	shell := fs.String("shell", "tmux:new:-A:-s:main", "launch command, ':'-separated")
	ice := iceFlags(fs)
	_ = fs.Parse(args)

	cfg, err := agent.LoadOrInit(*dir, *name, *signalURL)
	if err != nil {
		fatal(err)
	}
	launch := strings.Split(*shell, ":")
	if launch[0] == "tmux" && !agent.TmuxInstalled() {
		fatal(fmt.Errorf("tmux not installed (brew install tmux), or pass --shell sh"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt := agent.NewRuntime(cfg, launch, ice())
	rt.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "mir-agent: "+f+"\n", a...) }
	fmt.Printf("mir-agent up: machine %s, signaling %s\n", cfg.MachineID, cfg.SignalURL)
	// Non-blocking update notice (cache-only display; refresh in background while serving).
	selfupdate.New(repoSlug, "mir-agent").MaybeNotify(os.Stderr, updateCachePath(*dir), version.Version, 24*time.Hour)
	if err := rt.Up(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "machine"
	}
	return h
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// iceFlags registers --stun/--turn/--turn-user/--turn-pass on fs and returns a
// closure that builds the ICE server list (call it after fs.Parse). TURN is the
// opt-in symmetric-NAT fallback; Noise keeps it blind to content.
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

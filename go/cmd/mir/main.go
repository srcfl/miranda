// go/cmd/mir/main.go
package main

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

	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/peer"
	"github.com/srcful/terminal-relay/go/internal/sas"
	"github.com/srcful/terminal-relay/go/internal/version"
)

func defaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terminal-relay")
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "--version", "-v", "version":
		fmt.Println("mir", version.String())
		return
	case "keygen":
		cmdKeygen(os.Args[2:])
	case "pair":
		cmdPair(os.Args[2:])
	case "add-machine":
		cmdAddMachine(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "attach":
		cmdAttach(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mir <keygen|pair|add-machine|list|attach|run> [flags]")
	os.Exit(2)
}

// cmdRun attaches and runs one command non-interactively, streaming output for a
// short window. Useful for scripts and the NAT-sim smoke test (no TTY needed).
func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	ice := iceFlags(fs)
	window := fs.Duration("window", 3*time.Second, "how long to stream output before exiting")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		fatal(fmt.Errorf("usage: mir run <machine> <command...>"))
	}
	name := rest[0]
	cmd := strings.Join(rest[1:], " ")

	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}
	m, err := client.GetMachine(*dir, name)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, ice())
	if err != nil {
		fatal(err)
	}
	defer cleanup()
	if err := client.RunCommand(ctx, mc, sess, cmd, *window, os.Stdout); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("owner public key:\n  %s\n\nPin it on each machine:\n  mir-agent pair-dev --owner-pub %s\n", id.OwnerPubHex, id.OwnerPubHex)
}

func cmdPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal(fmt.Errorf("usage: mir pair <code>   (the code printed by `mir-agent pair`)"))
	}
	signalURL, token, err := pairing.DecodeCode(rest[0])
	if err != nil {
		fatal(err)
	}
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		fatal(err)
	}
	defer closeConn()

	info, binding, err := pairing.RunInitiator(ctx, mc, token, idn.OwnerPub())
	if err != nil {
		fatal(err)
	}
	m := client.Machine{Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL}
	if err := client.AddMachine(*dir, m); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ paired machine %q — try: mir attach %s\n", m.Name, m.Name)
	fmt.Printf("  safety number: %s  (must match the machine's)\n", sas.FromBinding(binding))
}

func cmdAddMachine(args []string) {
	fs := flag.NewFlagSet("add-machine", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", "", "machine name")
	id := fs.String("id", "", "machine id (from `mir-agent enroll`)")
	hostPub := fs.String("host-pub", "", "machine host public key (hex, from `mir-agent enroll`)")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL")
	_ = fs.Parse(args)
	if *name == "" || *id == "" || *hostPub == "" {
		fatal(fmt.Errorf("--name, --id and --host-pub are required"))
	}
	m := client.Machine{Name: *name, MachineID: *id, HostPubHex: strings.ToLower(*hostPub), SignalURL: *signalURL}
	if err := client.AddMachine(*dir, m); err != nil {
		fatal(err)
	}
	fmt.Printf("added machine %q (%s) via %s\n", m.Name, m.MachineID, m.SignalURL)
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	list, err := client.ListMachines(*dir)
	if err != nil {
		fatal(err)
	}
	if len(list) == 0 {
		fmt.Println("no machines yet — add one with `mir add-machine`")
		return
	}
	for _, m := range list {
		fmt.Printf("%-16s %s  %s\n", m.Name, m.MachineID, m.SignalURL)
	}
}

func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	prefixFlag := fs.String("prefix", "ctrl-o", "multiplexer switch key (e.g. ctrl-o, ctrl-a, ctrl-space)")
	ice := iceFlags(fs)
	_ = fs.Parse(args)
	names := fs.Args()
	if len(names) == 0 {
		fatal(fmt.Errorf("usage: mir attach <machine> [machine...]"))
	}
	prefix, prefixLabel, err := parsePrefix(*prefixFlag)
	if err != nil {
		fatal(err)
	}
	servers := ice()
	idn, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(names) == 1 {
		m, err := client.GetMachine(*dir, names[0])
		if err != nil {
			fatal(err)
		}
		mc, sess, cleanup, err := client.Attach(ctx, *m, idn, servers)
		if err != nil {
			fatal(err)
		}
		defer cleanup()
		if err := client.RunInteractive(ctx, mc, sess, m.Name); err != nil && ctx.Err() == nil {
			fatal(err)
		}
		return
	}

	sessions, cleanup, err := client.AttachAll(ctx, *dir, names, idn, servers)
	if err != nil {
		fatal(err)
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions, prefix, prefixLabel); err != nil && ctx.Err() == nil {
		fatal(err)
	}
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

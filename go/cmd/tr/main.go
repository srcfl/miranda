// go/cmd/tr/main.go
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
	case "keygen":
		cmdKeygen(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "usage: tr <keygen|add-machine|list|attach|run> [flags]")
	os.Exit(2)
}

// cmdRun attaches and runs one command non-interactively, streaming output for a
// short window. Useful for scripts and the NAT-sim smoke test (no TTY needed).
func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	stunFlag := fs.String("stun", "", "comma-separated STUN URLs")
	window := fs.Duration("window", 3*time.Second, "how long to stream output before exiting")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		fatal(fmt.Errorf("usage: tr run <machine> <command...>"))
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

	mc, sess, cleanup, err := client.Attach(ctx, *m, idn, splitStun(*stunFlag))
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
	fmt.Printf("owner public key:\n  %s\n\nPin it on each machine:\n  tr-agent pair-dev --owner-pub %s\n", id.OwnerPubHex, id.OwnerPubHex)
}

func cmdAddMachine(args []string) {
	fs := flag.NewFlagSet("add-machine", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", "", "machine name")
	id := fs.String("id", "", "machine id (from `tr-agent enroll`)")
	hostPub := fs.String("host-pub", "", "machine host public key (hex, from `tr-agent enroll`)")
	signalURL := fs.String("signal", "http://localhost:8443", "signaling server base URL")
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
		fmt.Println("no machines yet — add one with `tr add-machine`")
		return
	}
	for _, m := range list {
		fmt.Printf("%-16s %s  %s\n", m.Name, m.MachineID, m.SignalURL)
	}
}

func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	stunFlag := fs.String("stun", "", "comma-separated STUN URLs (e.g. stun:host:3478); empty = host candidates only")
	_ = fs.Parse(args)
	names := fs.Args()
	if len(names) == 0 {
		fatal(fmt.Errorf("usage: tr attach <machine> [machine...]"))
	}
	stun := splitStun(*stunFlag)
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
		mc, sess, cleanup, err := client.Attach(ctx, *m, idn, stun)
		if err != nil {
			fatal(err)
		}
		defer cleanup()
		if err := client.RunInteractive(ctx, mc, sess, m.Name); err != nil && ctx.Err() == nil {
			fatal(err)
		}
		return
	}

	sessions, cleanup, err := client.AttachAll(ctx, *dir, names, idn, stun)
	if err != nil {
		fatal(err)
	}
	defer cleanup()
	if err := client.RunInteractiveMux(ctx, sessions); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// splitStun turns a comma-separated STUN flag into a slice; empty -> nil (host
// candidates only).
func splitStun(s string) []string {
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

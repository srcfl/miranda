// Package cli is the shared command layer for the mir node. Both cmd/mir and the
// deprecated cmd/mir-agent shim dispatch through Run/RunAgentCompat, so the two
// binaries stay byte-for-byte identical in behavior — mir-agent only adds a
// deprecation notice and a different self-update asset label.
package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/srcful/terminal-relay/go/internal/version"
)

// app carries the I/O sinks and the running binary's identity through every
// handler. binary is "mir" normally and "mir-agent" via the shim; it selects the
// self-update release asset and labels update notices.
type app struct {
	in     io.Reader
	out    io.Writer // user-facing stdout
	errOut io.Writer // diagnostics, usage, update/deprecation notices
	binary string
}

// Run dispatches a `mir` invocation. argv is os.Args[1:] (no program name).
// Returns a process exit code.
func Run(argv []string, stdout, stderr io.Writer) int {
	return (&app{in: os.Stdin, out: stdout, errOut: stderr, binary: "mir"}).run(argv)
}

// runWithInput is the testable form used by secret-input commands. Production
// callers use Run, which wires stdin to os.Stdin.
func runWithInput(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return (&app{in: stdin, out: stdout, errOut: stderr, binary: "mir"}).run(argv)
}

const agentDeprecationNotice = "note: `mir-agent` is deprecated and now an alias for `mir` — use `mir up` / `mir pair`. This shim will be removed in a future release."

// RunAgentCompat is the deprecated mir-agent entry point: it prints a one-line
// deprecation notice to stderr, then dispatches exactly like Run but labelled
// "mir-agent" (so self-update fetches the mir-agent asset and notices read right).
func RunAgentCompat(argv []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, agentDeprecationNotice)
	return (&app{in: os.Stdin, out: stdout, errOut: stderr, binary: "mir-agent"}).run(argv)
}

// canonicalCommand maps tmux-style shorthands and friendly spellings onto the
// dispatch table's canonical names. An explicit table, not prefix matching, so
// what `mir a` does is greppable and can never change by adding a command.
func canonicalCommand(cmd string) string {
	switch cmd {
	case "a":
		return "attach"
	case "ls":
		return "list"
	case "id":
		return "identity"
	case "update":
		return "self-update"
	}
	return cmd
}

func (a *app) run(argv []string) int {
	if len(argv) == 0 {
		// On a terminal, `mir` alone opens your machines (the overview). For a
		// script or pipe the static guide + exit 2 stays exactly as it was.
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			return a.exit(a.cmdOverview())
		}
		a.guide()
		return 2
	}
	switch canonicalCommand(argv[0]) {
	case "--help", "-h", "help":
		a.guide()
		return 0
	case "--version", "-v", "version":
		fmt.Fprintln(a.out, a.binary, version.String())
		return 0
	case "keygen":
		return a.exit(a.cmdKeygen(argv[1:]))
	case "add-machine":
		return a.exit(a.cmdAddMachine(argv[1:]))
	case "list":
		return a.exit(a.cmdList(argv[1:]))
	case "attach":
		return a.exit(a.cmdAttach(argv[1:]))
	case "run":
		return a.exit(a.cmdRun(argv[1:]))
	case "self-update":
		return a.exit(a.cmdSelfUpdate(argv[1:]))
	case "enroll":
		return a.exit(a.cmdEnroll(argv[1:]))
	case "pair-dev":
		return a.exit(a.cmdPairDev(argv[1:]))
	case "up":
		return a.exit(a.cmdUp(argv[1:]))
	case "pair":
		return a.exit(a.cmdPair(argv[1:]))
	case "share":
		return a.exit(a.cmdShare(argv[1:]))
	case "join":
		return a.exit(a.cmdJoin(argv[1:]))
	case "identity":
		return a.exit(a.cmdIdentity(argv[1:]))
	case "machine":
		return a.exit(a.cmdMachine(argv[1:]))
	case "doctor":
		return a.exit(a.cmdDoctor(argv[1:]))
	case "wallet":
		return a.exit(a.cmdLegacyWallet(argv[1:]))
	default:
		a.usage()
		return 2
	}
}

// exit maps a handler error to an exit code, printing it like the old fatal().
func (a *app) exit(err error) int {
	if err != nil {
		fmt.Fprintln(a.errOut, "error:", err)
		return 1
	}
	return 0
}

func (a *app) usage() {
	fmt.Fprintln(a.errOut, "usage: "+a.binary+" <up|attach|list|pair|share|join|machine|identity|doctor|run|update|--version> [flags]")
	fmt.Fprintln(a.errOut, "shorthands: a = attach, ls = list, id = identity")
}

// guide is the no-argument landing: a friendly walkthrough of the core flow, with a
// first-run welcome when there is no config yet. It goes to stdout (it's help the
// user asked for), while terse usage on an unknown command stays on stderr.
func (a *app) guide() {
	b := a.binary
	p := func(s string) { fmt.Fprintln(a.out, s) }
	if freshSetup() {
		p("👋  Welcome to " + b + ". Looks like a fresh setup.")
		p("")
		p(b + " reaches the terminals already running on your machines, from any device.")
		p("No inbound ports or SSH keys. Your passkey identity and terminal data stay")
		p("end-to-end encrypted; targets hold only their own machine keys.")
		p("")
	}
	p(b + " — tmux keeps your session alive; " + b + " gets you to it from any device.")
	p("")
	p("  Serve a machine (on the box you want to reach):")
	p("    " + b + " up                keep its tmux sessions reachable — first run shows a pairing QR")
	p("    " + b + " pair              add another owner later — prints a QR + safety number")
	p("")
	p("  Reach your machines (where you are):")
	p("    " + b + "                   your machines, live — Enter attaches (Ctrl-O then d comes back)")
	p("    " + b + " pair <code>       pair to a machine (compare the safety numbers)")
	p("    " + b + " attach            continue where you left off — short: " + b + " a")
	p("    " + b + " attach <name>     open its shell, peer-to-peer — short: " + b + " a <name>")
	p("    " + b + " attach a b c      several at once — Ctrl-O then 1–9 to switch")
	p("")
	p("  Share a terminal (guests, time-boxed):")
	p("    " + b + " share <machine>   invite someone in — read-only, expires in an hour")
	p("    " + b + " share ls          your invites; revoke one: " + b + " share revoke <id>")
	p("    " + b + " join <code>       claim an invite someone sent you")
	p("")
	p("  Identity & machines:")
	p("    " + b + " identity show     your Miranda owner id — short: " + b + " id show")
	p("    " + b + " identity export-recovery   emergency recovery phrase")
	p("    " + b + " list              machines you've paired — short: " + b + " ls")
	p("    " + b + " machine rename <name> <new-name>   rename it on every device")
	p("    " + b + " machine revoke <name>   retire a machine — asks first (--yes for scripts)")
	p("    " + b + " doctor            verify local state, keychain, tmux and relay")
	p("    " + b + " doctor --share    the same checks as a paste-safe report for an issue")
	p("    " + b + " update            install the newest release (checked once a day)")
	p("")
	p("Connections go machine-to-machine when they can — same network or hole-punched")
	p("across the internet — and fall back to a relay only when they must.")
	p("Full help for any command: " + b + " <command> -h")
	// The guide is the one place a user stops to look around, so spend up to a
	// second checking for an update — bounded, cached for a day, silent on any
	// failure, and skipped when stdin isn't a terminal (scripts, CI, go test).
	if term.IsTerminal(int(os.Stdin.Fd())) {
		updateClient(b).NoticeNow(a.errOut, updateCachePath(defaultClientDir()),
			version.Version, 24*time.Hour, time.Second)
	}
}

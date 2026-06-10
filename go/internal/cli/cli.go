// Package cli is the shared command layer for the mir node. Both cmd/mir and the
// deprecated cmd/mir-agent shim dispatch through Run/RunAgentCompat, so the two
// binaries stay byte-for-byte identical in behavior — mir-agent only adds a
// deprecation notice and a different self-update asset label.
package cli

import (
	"fmt"
	"io"

	"github.com/srcful/terminal-relay/go/internal/version"
)

// app carries the I/O sinks and the running binary's identity through every
// handler. binary is "mir" normally and "mir-agent" via the shim; it selects the
// self-update release asset and labels update notices.
type app struct {
	out    io.Writer // user-facing stdout
	errOut io.Writer // diagnostics, usage, update/deprecation notices
	binary string
}

// Run dispatches a `mir` invocation. argv is os.Args[1:] (no program name).
// Returns a process exit code.
func Run(argv []string, stdout, stderr io.Writer) int {
	return (&app{out: stdout, errOut: stderr, binary: "mir"}).run(argv)
}

const agentDeprecationNotice = "note: `mir-agent` is deprecated and now an alias for `mir` — use `mir up` / `mir pair` / `mir enroll`. This shim will be removed in a future release."

// RunAgentCompat is the deprecated mir-agent entry point: it prints a one-line
// deprecation notice to stderr, then dispatches exactly like Run but labelled
// "mir-agent" (so self-update fetches the mir-agent asset and notices read right).
func RunAgentCompat(argv []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, agentDeprecationNotice)
	return (&app{out: stdout, errOut: stderr, binary: "mir-agent"}).run(argv)
}

func (a *app) run(argv []string) int {
	if len(argv) == 0 {
		a.usage()
		return 2
	}
	switch argv[0] {
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
	fmt.Fprintln(a.errOut, "usage: "+a.binary+" <up|attach|list|pair|enroll|pair-dev|keygen|add-machine|run|self-update|--version> [flags]")
}

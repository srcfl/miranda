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

func (a *app) run(argv []string) int {
	if len(argv) == 0 {
		a.usage()
		return 2
	}
	switch argv[0] {
	case "--version", "-v", "version":
		fmt.Fprintln(a.out, a.binary, version.String())
		return 0
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

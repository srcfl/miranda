package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// pkgManager is one way to install tmux on this machine: the binary that
// provides it and the exact arguments to install tmux with it.
type pkgManager struct {
	bin       string   // binary name; also what lookPath checks for
	args      []string // arguments after bin, e.g. {"install", "-y", "tmux"}
	needsSudo bool     // true for Linux system package managers; brew never needs it
}

// command is the install command as a human reads it, e.g. "brew install tmux".
func (p pkgManager) command() string {
	return strings.Join(append([]string{p.bin}, p.args...), " ")
}

// runCommand is the command a user without root must run themselves.
func (p pkgManager) runCommand() string {
	if p.needsSudo {
		return "sudo " + p.command()
	}
	return p.command()
}

// linuxPkgManagers are probed in this order: apt-get and apt first (Debian/
// Ubuntu, the common case), then dnf (Fedora/RHEL), then pacman (Arch).
var linuxPkgManagers = []pkgManager{
	{bin: "apt-get", args: []string{"install", "-y", "tmux"}, needsSudo: true},
	{bin: "apt", args: []string{"install", "-y", "tmux"}, needsSudo: true},
	{bin: "dnf", args: []string{"install", "-y", "tmux"}, needsSudo: true},
	{bin: "pacman", args: []string{"-S", "--noconfirm", "tmux"}, needsSudo: true},
}

// tmuxWhy is the one-line reason shown before offering to install tmux.
const tmuxWhy = "tmux keeps your session running after you disconnect — worth having."

// tmuxFallbackNote is the one honest line shown whenever `mir up` proceeds
// without tmux for this run.
const tmuxFallbackNote = "Continuing with a plain shell — sessions end when you disconnect."

// fallbackLaunch is the plain, non-persistent shell `mir up` falls back to for
// a single run when tmux is missing and not installed. "sh" matches the
// existing --shell sh escape hatch documented elsewhere in this package.
var fallbackLaunch = []string{"sh"}

// tmuxBootstrap decides whether `mir up` can launch tmux, offering to install
// it when it is missing. Every OS/process dependency is a field (not a direct
// os/exec call) so the whole decision tree — detection, consent, the no-sudo
// path, the fallback — unit-tests without touching a real package manager.
type tmuxBootstrap struct {
	lookPath func(string) (string, error) // exec.LookPath in production
	run      func(bin string, args []string, out, errOut io.Writer) error
	geteuid  func() int
	goos     string
	isTTY    bool
	in       io.Reader
}

// newTmuxBootstrap builds a production tmuxBootstrap: real PATH lookups, a
// real subprocess runner (its output streamed to out/errOut so the user sees
// brew/apt's own progress), the real euid, and the real GOOS.
func newTmuxBootstrap(isTTY bool, in io.Reader) *tmuxBootstrap {
	return &tmuxBootstrap{
		lookPath: exec.LookPath,
		run: func(bin string, args []string, out, errOut io.Writer) error {
			cmd := exec.Command(bin, args...)
			cmd.Stdout, cmd.Stderr = out, errOut
			return cmd.Run()
		},
		geteuid: os.Geteuid,
		goos:    runtime.GOOS,
		isTTY:   isTTY,
		in:      in,
	}
}

// detect returns the first available package manager for this OS, or nil if
// none of the ones we know about is on PATH.
func (b *tmuxBootstrap) detect() *pkgManager {
	switch b.goos {
	case "darwin":
		if _, err := b.lookPath("brew"); err == nil {
			return &pkgManager{bin: "brew", args: []string{"install", "tmux"}}
		}
	case "linux":
		for _, pm := range linuxPkgManagers {
			if _, err := b.lookPath(pm.bin); err == nil {
				pm := pm
				return &pm
			}
		}
	}
	return nil
}

// installHint carries the exact install command (or, absent a known package
// manager, an honest admission) into a refusal message.
func installHint(pm *pkgManager) string {
	if pm == nil {
		return "no supported package manager found (checked brew, apt-get, apt, dnf, pacman); install tmux manually"
	}
	return "run `" + pm.runCommand() + "`"
}

// ensure makes launch runnable: if it does not start tmux, or tmux is already
// on PATH, it is returned unchanged. Otherwise:
//   - no TTY: refused (fail closed — a scripted/systemd run cannot answer a
//     prompt), with the exact install command for this platform in the error.
//   - TTY, a package manager found, user accepts: installs it. Root (or brew,
//     which never needs root) runs the command directly; a non-root Linux user
//     is never sudo'd on their behalf — the exact command is printed and `mir
//     up` stops so they can run it themselves and try again.
//   - TTY, declined, no package manager, or the install did not work: falls
//     back to a plain shell for this run only (never persisted) with one
//     honest line about what that costs.
func (b *tmuxBootstrap) ensure(out, errOut io.Writer, launch []string) ([]string, error) {
	if launch[0] != "tmux" {
		return launch, nil
	}
	if _, err := b.lookPath("tmux"); err == nil {
		return launch, nil
	}
	pm := b.detect()
	if !b.isTTY {
		return nil, fmt.Errorf("tmux is not installed: %s, then re-run `mir up`; or pass --shell sh for a plain (non-persistent) shell", installHint(pm))
	}

	fmt.Fprintln(out, tmuxWhy)
	if pm == nil {
		fmt.Fprintln(out, "No supported package manager found (checked brew, apt-get, apt, dnf, pacman).")
		fmt.Fprintln(out, tmuxFallbackNote)
		return fallbackLaunch, nil
	}

	fmt.Fprintf(out, "Install tmux with `%s`? [Y/n] ", pm.command())
	line, _ := bufio.NewReader(b.in).ReadString('\n')
	if !installConsent(line) {
		fmt.Fprintln(out, tmuxFallbackNote)
		return fallbackLaunch, nil
	}

	if pm.needsSudo && b.geteuid() != 0 {
		fmt.Fprintf(out, "Not running as root — run this yourself, then re-run `mir up`:\n  %s\n", pm.runCommand())
		return nil, fmt.Errorf("tmux install needs root: run `%s`, then re-run `mir up`", pm.runCommand())
	}

	fmt.Fprintf(out, "Installing tmux (%s)…\n", pm.bin)
	if err := b.run(pm.bin, pm.args, out, errOut); err != nil {
		fmt.Fprintf(out, "tmux install failed: %v\n", err)
		fmt.Fprintln(out, tmuxFallbackNote)
		return fallbackLaunch, nil
	}
	if _, err := b.lookPath("tmux"); err != nil {
		fmt.Fprintln(out, "tmux still isn't on PATH — you may need to restart your shell.")
		fmt.Fprintln(out, tmuxFallbackNote)
		return fallbackLaunch, nil
	}
	fmt.Fprintln(out, "tmux installed.")
	return launch, nil
}

// installConsent parses a [Y/n] answer: empty (just Enter) and "y"/"yes" mean
// yes; anything else means no. Default-yes matches the [Y/n] prompt — the
// opposite of the [y/N] safety-number gate, which defaults to no because that
// one guards a trust decision, not a package install.
func installConsent(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

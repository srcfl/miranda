package cli

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// lookPathOnly returns a lookPath stub that reports found for exactly the
// given binaries — everything else is "not found", the same shape
// exec.LookPath returns for a binary absent from PATH.
func lookPathOnly(found ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, f := range found {
		set[f] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestDetectPkgManager(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		found    []string
		wantBin  string
		wantNone bool
	}{
		{name: "macOS with brew", goos: "darwin", found: []string{"brew"}, wantBin: "brew"},
		{name: "macOS without brew", goos: "darwin", found: nil, wantNone: true},
		{name: "linux apt-get preferred over apt", goos: "linux", found: []string{"apt-get", "apt"}, wantBin: "apt-get"},
		{name: "linux apt only", goos: "linux", found: []string{"apt"}, wantBin: "apt"},
		{name: "linux dnf", goos: "linux", found: []string{"dnf"}, wantBin: "dnf"},
		{name: "linux pacman", goos: "linux", found: []string{"pacman"}, wantBin: "pacman"},
		{name: "linux nothing known", goos: "linux", found: []string{"snap"}, wantNone: true},
		{name: "unsupported goos", goos: "windows", found: []string{"brew", "apt-get"}, wantNone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &tmuxBootstrap{lookPath: lookPathOnly(tc.found...), goos: tc.goos}
			pm := b.detect()
			if tc.wantNone {
				if pm != nil {
					t.Fatalf("detect() = %+v, want nil", pm)
				}
				return
			}
			if pm == nil || pm.bin != tc.wantBin {
				t.Fatalf("detect() = %+v, want bin %q", pm, tc.wantBin)
			}
		})
	}
}

func TestPkgManagerCommands(t *testing.T) {
	brew := pkgManager{bin: "brew", args: []string{"install", "tmux"}, needsSudo: false}
	if got, want := brew.command(), "brew install tmux"; got != want {
		t.Errorf("brew.command() = %q, want %q", got, want)
	}
	if got, want := brew.runCommand(), "brew install tmux"; got != want {
		t.Errorf("brew never needs sudo: runCommand() = %q, want %q", got, want)
	}

	apt := pkgManager{bin: "apt-get", args: []string{"install", "-y", "tmux"}, needsSudo: true}
	if got, want := apt.runCommand(), "sudo apt-get install -y tmux"; got != want {
		t.Errorf("apt.runCommand() = %q, want %q", got, want)
	}
}

func TestInstallConsent(t *testing.T) {
	yes := []string{"", "\n", "y", "Y", "yes", "YES", "  y  \n"}
	no := []string{"n", "N", "no", "nope", "whatever"}
	for _, s := range yes {
		if !installConsent(s) {
			t.Errorf("installConsent(%q) = false, want true (default-yes on [Y/n])", s)
		}
	}
	for _, s := range no {
		if installConsent(s) {
			t.Errorf("installConsent(%q) = true, want false", s)
		}
	}
}

func TestEnsureSkipsNonTmuxLaunch(t *testing.T) {
	b := &tmuxBootstrap{lookPath: lookPathOnly() /* nothing on PATH, tmux included */}
	got, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"sh"})
	if err != nil || len(got) != 1 || got[0] != "sh" {
		t.Fatalf("ensure(sh) = %v, %v; want unchanged launch, no error", got, err)
	}
}

func TestEnsureNoopWhenTmuxAlreadyInstalled(t *testing.T) {
	b := &tmuxBootstrap{lookPath: lookPathOnly("tmux")}
	launch := []string{"tmux", "new", "-A", "-s", "main"}
	got, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, launch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ":") != strings.Join(launch, ":") {
		t.Fatalf("ensure() = %v, want unchanged %v", got, launch)
	}
}

// TestEnsureNonTTYRefusesWithExactCommand pins point 3 of the spec: a
// scripted/systemd run must still refuse (fail closed, no weakened
// semantics) but the message must carry the exact install command detected
// for this platform.
func TestEnsureNonTTYRefusesWithExactCommand(t *testing.T) {
	b := &tmuxBootstrap{
		lookPath: lookPathOnly("apt-get"),
		goos:     "linux",
		isTTY:    false,
	}
	_, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"tmux", "new", "-A", "-s", "main"})
	if err == nil {
		t.Fatal("expected a refusal for a non-TTY run with tmux missing")
	}
	if !strings.Contains(err.Error(), "sudo apt-get install -y tmux") {
		t.Fatalf("refusal must carry the exact install command, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--shell sh") {
		t.Fatalf("refusal must still mention the --shell sh escape hatch, got: %v", err)
	}
}

// TestEnsureNonTTYNoPackageManagerFound covers the honest-admission branch of
// the non-TTY message when nothing recognized is on PATH.
func TestEnsureNonTTYNoPackageManagerFound(t *testing.T) {
	b := &tmuxBootstrap{lookPath: lookPathOnly(), goos: "linux", isTTY: false}
	_, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"tmux", "new", "-A", "-s", "main"})
	if err == nil || !strings.Contains(err.Error(), "no supported package manager found") {
		t.Fatalf("expected the no-package-manager refusal, got %v", err)
	}
}

// TestEnsureTTYAcceptsAndInstallsAsRoot covers the accept path where the
// process is already root (Linux) — the install command runs directly, no
// sudo is invoked, and it verifies tmux afterwards before handing back the
// original tmux launch.
func TestEnsureTTYAcceptsAndInstallsAsRoot(t *testing.T) {
	installed := false
	lookPath := func(name string) (string, error) {
		if name == "tmux" && installed {
			return "/usr/bin/tmux", nil
		}
		if name == "apt-get" {
			return "/usr/bin/apt-get", nil
		}
		return "", exec.ErrNotFound
	}
	ran := false
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPath,
		run: func(bin string, args []string, _, _ io.Writer) error {
			ran = true
			if bin != "apt-get" {
				t.Fatalf("ran %q, want apt-get", bin)
			}
			installed = true
			return nil
		},
		geteuid: func() int { return 0 },
		goos:    "linux",
		isTTY:   true,
		in:      strings.NewReader("y\n"),
	}
	launch := []string{"tmux", "new", "-A", "-s", "main"}
	got, err := b.ensure(out, out, launch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("expected the install command to run for a root process")
	}
	if strings.Join(got, ":") != strings.Join(launch, ":") {
		t.Fatalf("ensure() = %v, want the original tmux launch %v", got, launch)
	}
	if !strings.Contains(out.String(), "tmux installed") {
		t.Fatalf("expected a confirmation line, got:\n%s", out.String())
	}
}

// TestEnsureTTYAcceptsBrewNeverNeedsSudo mirrors the root case for macOS/brew,
// which never needs sudo regardless of euid.
func TestEnsureTTYAcceptsBrewNeverNeedsSudo(t *testing.T) {
	installed := false
	lookPath := func(name string) (string, error) {
		if name == "tmux" && installed {
			return "/usr/local/bin/tmux", nil
		}
		if name == "brew" {
			return "/usr/local/bin/brew", nil
		}
		return "", exec.ErrNotFound
	}
	ran := false
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPath,
		run: func(bin string, args []string, _, _ io.Writer) error {
			ran = true
			installed = true
			return nil
		},
		geteuid: func() int { return 501 }, // ordinary, non-root user
		goos:    "darwin",
		isTTY:   true,
		in:      strings.NewReader("\n"), // bare Enter: default-yes
	}
	got, err := b.ensure(out, out, []string{"tmux", "new", "-A", "-s", "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("brew must run directly without sudo")
	}
	if got[0] != "tmux" {
		t.Fatalf("ensure() = %v, want tmux launch restored", got)
	}
}

// TestEnsureTTYAcceptsNonRootLinuxNeverRunsSudo pins the safety-critical rule:
// this process must NEVER invoke sudo on the user's behalf. It must print the
// exact command and stop instead.
func TestEnsureTTYAcceptsNonRootLinuxNeverRunsSudo(t *testing.T) {
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPathOnly("apt-get"),
		run: func(bin string, args []string, _, _ io.Writer) error {
			t.Fatalf("must not run anything (no sudo on the user's behalf): %s %v", bin, args)
			return nil
		},
		geteuid: func() int { return 1000 }, // not root
		goos:    "linux",
		isTTY:   true,
		in:      strings.NewReader("y\n"),
	}
	_, err := b.ensure(out, out, []string{"tmux", "new", "-A", "-s", "main"})
	if err == nil {
		t.Fatal("expected mir up to stop rather than sudo on the user's behalf")
	}
	want := "sudo apt-get install -y tmux"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error must carry the exact command, got: %v", err)
	}
	if !strings.Contains(out.String(), want) {
		t.Fatalf("printed output must carry the exact command too, got:\n%s", out.String())
	}
}

// TestEnsureTTYDeclineFallsBackForThisRunOnly covers the chosen UX for a "n"
// answer: fall back to a plain shell for this run, with the honest one-liner,
// and never touch the install command.
func TestEnsureTTYDeclineFallsBackForThisRunOnly(t *testing.T) {
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPathOnly("apt-get"),
		run: func(bin string, args []string, _, _ io.Writer) error {
			t.Fatalf("declined install must never run a command: %s %v", bin, args)
			return nil
		},
		goos:  "linux",
		isTTY: true,
		in:    strings.NewReader("n\n"),
	}
	got, err := b.ensure(out, out, []string{"tmux", "new", "-A", "-s", "main"})
	if err != nil {
		t.Fatalf("decline must not error, got %v", err)
	}
	if strings.Join(got, ":") != "sh" {
		t.Fatalf("ensure() = %v, want the plain-shell fallback", got)
	}
	if !strings.Contains(out.String(), "sessions end when you disconnect") {
		t.Fatalf("expected the honest fallback line, got:\n%s", out.String())
	}
}

// TestEnsureTTYNoPackageManagerFallsBack covers the other fallback trigger:
// nothing recognized on PATH, so there is nothing to offer to install.
func TestEnsureTTYNoPackageManagerFallsBack(t *testing.T) {
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPathOnly(),
		goos:     "linux",
		isTTY:    true,
		in:       strings.NewReader(""),
	}
	got, err := b.ensure(out, out, []string{"tmux", "new", "-A", "-s", "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(got, ":") != "sh" {
		t.Fatalf("ensure() = %v, want the plain-shell fallback", got)
	}
	if !strings.Contains(out.String(), "No supported package manager found") {
		t.Fatalf("expected the no-manager admission, got:\n%s", out.String())
	}
}

// TestEnsureTTYInstallFailureFallsBack: an install command that runs but
// fails (network down, package not found, whatever) must degrade to the
// fallback rather than error the whole `mir up` out.
func TestEnsureTTYInstallFailureFallsBack(t *testing.T) {
	out := &bytes.Buffer{}
	b := &tmuxBootstrap{
		lookPath: lookPathOnly("brew"), // tmux never appears afterwards
		run: func(bin string, args []string, _, _ io.Writer) error {
			return fmt.Errorf("exit status 1")
		},
		geteuid: func() int { return 501 },
		goos:    "darwin",
		isTTY:   true,
		in:      strings.NewReader("y\n"),
	}
	got, err := b.ensure(out, out, []string{"tmux", "new", "-A", "-s", "main"})
	if err != nil {
		t.Fatalf("a failed install must fall back, not error, got %v", err)
	}
	if strings.Join(got, ":") != "sh" {
		t.Fatalf("ensure() = %v, want the plain-shell fallback", got)
	}
	if !strings.Contains(out.String(), "tmux install failed") {
		t.Fatalf("expected the failure to be reported, got:\n%s", out.String())
	}
}

func TestNewTmuxBootstrapWiresRealDependencies(t *testing.T) {
	b := newTmuxBootstrap(false, strings.NewReader(""))
	if b.lookPath == nil || b.run == nil || b.geteuid == nil || b.goos == "" {
		t.Fatalf("newTmuxBootstrap left a field unset: %+v", b)
	}
	if _, err := b.lookPath("tmux"); err != nil && err != exec.ErrNotFound {
		// exec.LookPath returns a *exec.Error wrapping ErrNotFound, not
		// ErrNotFound itself; just confirm it's callable without panicking.
		_ = err
	}
}

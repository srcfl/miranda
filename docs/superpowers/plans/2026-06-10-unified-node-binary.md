# Unified Node Binary (Track A1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge the `mir` (client) and `mir-agent` (server) binaries into one symmetric `mir` binary with all subcommands, reducing `mir-agent` to a thin deprecation shim — behavior-preserving, no crypto or network changes.

**Architecture:** Extract a new `internal/cli` package that owns all command dispatch and handlers behind a testable `Run(argv, stdout, stderr) int`. Both `cmd/mir` and `cmd/mir-agent` become 3-line wrappers that call into it (`Run` vs `RunAgentCompat`). An `app` struct threads the I/O sinks and the running binary's identity ("mir" / "mir-agent") through every handler, so self-update picks the right release asset and update notices are labelled correctly. Handlers return `error` instead of calling `os.Exit`, which makes them unit-testable; `Run` centralizes the error→exit-code mapping.

**Tech Stack:** Go 1.26, stdlib `flag`/`io`/`testing`, existing `internal/{client,agent,pairing,sas,peer,defaults,selfupdate,version}` packages. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-10-north-star-mesh-wallet-identity-design.md` (§2 "Unified symmetric node", roadmap Track A1).

---

## Conventions used in this plan

This is largely a **behavior-preserving refactor** (moving existing handlers, not rewriting them). To stay honest without pasting hundreds of unchanged lines:

- **New logic** (the `app` struct, `Run`/`run`/`exit`/`usage`, `classifyPair`, `RunAgentCompat`, every test) is shown in full.
- **Moves** are specified as: exact source coordinates + a fixed set of mechanical edits + one fully-worked before/after example so the pattern is unambiguous. The mechanical edits for every moved handler are:
  1. Signature `func cmdX(args []string)` → `func (a *app) cmdX(args []string) error`.
  2. Each `fatal(err)` / `fatal(fmt.Errorf(...))` → `return err` / `return fmt.Errorf(...)`.
  3. User-facing `fmt.Printf/Println(...)` (stdout) → `fmt.Fprintf/Fprintln(a.out, ...)`.
  4. Add a trailing `return nil` on the success path.
  5. `selfupdate.New(repoSlug, "mir"|"mir-agent")` → `selfupdate.New(repoSlug, a.binary)`.
- Worktree/branch: execute on an isolated branch (the execution skill creates it via `superpowers:using-git-worktrees`). Suggested name: `unified-node-binary`.
- **All `go` commands run from `go/`** (the module root): prefix with `cd /Users/fredde/repositories/miranda/go &&`.

---

## File Structure

| File | Responsibility |
|---|---|
| `go/internal/cli/cli.go` (create) | `app` struct, `Run`, `RunAgentCompat`, `run` dispatch, `exit`, `usage`, version handling |
| `go/internal/cli/shared.go` (create) | moved shared helpers: `defaultDir`, `repoSlug`, `updateCachePath`, `iceFlags`, `splitCSV`, `parsePrefix`, `hostname` |
| `go/internal/cli/client_cmds.go` (create) | moved client handlers: `cmdKeygen`, `cmdAddMachine`, `cmdList`, `cmdAttach`, `cmdRun`, `cmdSelfUpdate` |
| `go/internal/cli/agent_cmds.go` (create) | moved agent handlers: `cmdEnroll`, `cmdPairDev`, `cmdUp`, `autoUpdateLoop` |
| `go/internal/cli/pair.go` (create) | unified `cmdPair` + `classifyPair` (resolves the responder/initiator collision) |
| `go/internal/cli/cli_test.go` (create) | dispatch + non-blocking command tests |
| `go/internal/cli/shared_test.go` (create) | `splitCSV`, `parsePrefix` unit tests |
| `go/internal/cli/pair_test.go` (create) | `classifyPair` unit tests |
| `go/cmd/mir/main.go` (replace) | `os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))` |
| `go/cmd/mir-agent/main.go` (replace) | `os.Exit(cli.RunAgentCompat(os.Args[1:], os.Stdout, os.Stderr))` |
| `README.md` (modify) | document `mir up` etc.; note `mir-agent` is a deprecated alias |

---

## Task 1: Scaffold `internal/cli` — dispatch skeleton, version, usage

**Files:**
- Create: `go/internal/cli/cli.go`
- Test: `go/internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/version"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "mir ") || !strings.Contains(out.String(), version.Version) {
		t.Fatalf("version output = %q", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"wat"}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", errb.String())
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/`
Expected: FAIL — `undefined: Run` (package does not compile yet).

- [ ] **Step 3: Write the minimal dispatch skeleton**

Create `go/internal/cli/cli.go`. Only `--version`/`version`/unknown/empty are wired now; the real subcommands return a "not wired yet" error placeholder that later tasks replace with real handler calls.

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/cli.go go/internal/cli/cli_test.go && git commit -m "feat(cli): dispatch skeleton with version + usage"
```

---

## Task 2: Move shared helpers into `internal/cli/shared.go`

**Files:**
- Create: `go/internal/cli/shared.go`
- Test: `go/internal/cli/shared_test.go`
- Source: `go/cmd/mir/main.go` (`defaultDir` 24-27, `repoSlug` 29, `updateCachePath` 31-33, `iceFlags` 275-290, `splitCSV` 293-305, `parsePrefix` 309-324), `go/cmd/mir-agent/main.go` (`hostname` 244-250)

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/shared_test.go`:

```go
package cli

import "testing"

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":            nil,
		"  ":          nil,
		"a":           {"a"},
		"a,b,c":       {"a", "b", "c"},
		" a , b ,, c": {"a", "b", "c"}, // trims, drops empties
	}
	for in, want := range cases {
		got := splitCSV(in)
		if len(got) != len(want) {
			t.Fatalf("splitCSV(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitCSV(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestParsePrefix(t *testing.T) {
	ok := map[string]byte{"ctrl-o": 0x0f, "c-a": 0x01, "^o": 0x0f, "ctrl-space": 0x00, "ctrl-]": 0x1d}
	for in, want := range ok {
		b, _, err := parsePrefix(in)
		if err != nil || b != want {
			t.Fatalf("parsePrefix(%q) = %#x, %v, want %#x", in, b, err, want)
		}
	}
	if _, _, err := parsePrefix("ctrl-99"); err == nil {
		t.Fatal("parsePrefix(ctrl-99) should error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run 'SplitCSV|ParsePrefix'`
Expected: FAIL — `undefined: splitCSV` / `undefined: parsePrefix`.

- [ ] **Step 3: Create `shared.go` with the moved helpers**

Create `go/internal/cli/shared.go`. Move the listed functions **verbatim** from the source files (they have no `fatal`/stdout dependencies, so no transformation is needed — they move as-is). `repoSlug` keeps its value `"srcfl/miranda"`. Required imports: `flag`, `fmt`, `os`, `path/filepath`, `strings`, plus `github.com/srcful/terminal-relay/go/internal/{defaults,peer}`.

```go
package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/peer"
)

const repoSlug = "srcfl/miranda"

func defaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terminal-relay")
}

func updateCachePath(dir string) string { return filepath.Join(dir, "update-check.json") }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "machine"
	}
	return h
}

// iceFlags registers --stun/--turn/--turn-user/--turn-pass on fs and returns a
// closure building the ICE server list (call after fs.Parse).
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run 'SplitCSV|ParsePrefix'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/shared.go go/internal/cli/shared_test.go && git commit -m "feat(cli): move shared CLI helpers with tests"
```

---

## Task 3: Move client handlers into `internal/cli/client_cmds.go`

**Files:**
- Create: `go/internal/cli/client_cmds.go`
- Modify: `go/internal/cli/cli.go` (wire dispatch cases)
- Test: `go/internal/cli/cli_test.go` (append)
- Source: `go/cmd/mir/main.go` — `cmdSelfUpdate` 67-91, `cmdRun` 95-128, `cmdKeygen` 130-139, `cmdAddMachine` 178-194, `cmdList` 196-213, `cmdAttach` 215-265

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/cli/cli_test.go`:

```go
func TestKeygenPrintsOwnerKey(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"keygen", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "owner public key") {
		t.Fatalf("keygen output = %q", out.String())
	}
}

func TestListEmptyThenAddMachine(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "no machines yet") {
		t.Fatalf("empty list = %q", out.String())
	}
	out.Reset()
	add := []string{"add-machine", "--dir", dir, "--name", "box", "--id", "m1",
		"--host-pub", "aabbcc", "--signal", "https://relay.example"}
	if code := Run(add, &out, &errb); code != 0 {
		t.Fatalf("add exit = %d, stderr = %q", code, errb.String())
	}
	out.Reset()
	if code := Run([]string{"list", "--dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("list2 exit = %d", code)
	}
	if !strings.Contains(out.String(), "box") || !strings.Contains(out.String(), "m1") {
		t.Fatalf("list after add = %q", out.String())
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run 'Keygen|ListEmpty'`
Expected: FAIL — exit 2 (commands not wired; dispatch hits `default`).

- [ ] **Step 3: Create `client_cmds.go` by moving the six handlers**

Create `go/internal/cli/client_cmds.go`. Move the six functions from `go/cmd/mir/main.go`, applying the mechanical edits from "Conventions". Imports needed: `context`, `flag`, `fmt`, `os`, `os/signal`, `path/filepath`, `strings`, `syscall`, `time`, and `internal/{client,sas,selfupdate,version}`.

Worked example — `cmdKeygen` before (cmd/mir/main.go:130-139) → after:

```go
// BEFORE (package main, calls fatal):
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

// AFTER (method on app, returns error, writes to a.out):
func (a *app) cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	_ = fs.Parse(args)
	id, err := client.LoadOrCreateIdentity(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "owner public key:\n  %s\n\nPin it on each machine:\n  mir pair-dev --owner-pub %s\n", id.OwnerPubHex, id.OwnerPubHex)
	return nil
}
```

Apply the same transformation to `cmdAddMachine`, `cmdList`, `cmdAttach`, `cmdRun`, `cmdSelfUpdate`. Function-specific notes:
- **`cmdList`** and **`cmdAttach`**: change the update-notice line `selfupdate.New(repoSlug, "mir").MaybeNotify(os.Stderr, updateCachePath(*dir), version.Version, 24*time.Hour)` → `selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)`.
- **`cmdSelfUpdate`**: replace the binary-named messages and `selfupdate.New(repoSlug, "mir")` with `a.binary`:

```go
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
```
- **`cmdAttach`** / **`cmdRun`**: their interactive terminal I/O stays on `os.Stdin`/`os.Stdout` (handled inside `internal/client`); only convert incidental `fmt.Printf` (none in `cmdRun`; `cmdAttach` has none besides the notice) and `fatal(err)` → `return err`. Keep `signal.NotifyContext`, `client.Attach`, `client.RunInteractive`, `client.AttachAll`, `client.RunInteractiveMux`, `client.RunCommand` calls unchanged. The `os/signal` import is aliased as `signal` exactly as in the source.

- [ ] **Step 4: Wire the dispatch cases**

In `go/internal/cli/cli.go`, add these cases to the `switch` in `run`, above `default`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/`
Expected: PASS (all tests so far).

- [ ] **Step 6: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/client_cmds.go go/internal/cli/cli.go go/internal/cli/cli_test.go && git commit -m "feat(cli): move client commands (keygen/list/add-machine/attach/run/self-update)"
```

---

## Task 4: Move agent handlers into `internal/cli/agent_cmds.go`

**Files:**
- Create: `go/internal/cli/agent_cmds.go`
- Modify: `go/internal/cli/cli.go` (wire `enroll`, `pair-dev`, `up`)
- Test: `go/internal/cli/cli_test.go` (append)
- Source: `go/cmd/mir-agent/main.go` — `cmdEnroll` 92-110, `cmdPairDev` 112-124, `cmdUp` 171-204, `autoUpdateLoop` 209-242

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/cli/cli_test.go`:

```go
func TestEnrollPrintsMachineID(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := Run([]string{"enroll", "--dir", dir, "--name", "testbox", "--signal", "https://relay.example"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "machine_id:") || !strings.Contains(out.String(), "testbox") {
		t.Fatalf("enroll output = %q", out.String())
	}
}

func TestPairDevPinsOwner(t *testing.T) {
	t.Setenv("MIR_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	var out, errb bytes.Buffer
	// enroll first so the config dir is initialized
	if code := Run([]string{"enroll", "--dir", dir, "--name", "b", "--signal", "https://relay.example"}, &out, &errb); code != 0 {
		t.Fatalf("enroll exit = %d", code)
	}
	out.Reset()
	owner := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if code := Run([]string{"pair-dev", "--dir", dir, "--owner-pub", owner}, &out, &errb); code != 0 {
		t.Fatalf("pair-dev exit = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "pinned owner") {
		t.Fatalf("pair-dev output = %q", out.String())
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run 'Enroll|PairDev'`
Expected: FAIL — exit 2 (not wired).

- [ ] **Step 3: Create `agent_cmds.go` by moving the handlers**

Create `go/internal/cli/agent_cmds.go`. Move `cmdEnroll`, `cmdPairDev`, `cmdUp`, `autoUpdateLoop` from `go/cmd/mir-agent/main.go` with the mechanical edits. Imports: `context`, `flag`, `fmt`, `os`, `os/signal`, `path/filepath`, `strings`, `syscall`, `time`, the `qrterminal` package is **not** needed here (it belongs to pair — Task 5), and `internal/{agent,selfupdate,version}`.

Function-specific notes:
- **`cmdEnroll`**: `fmt.Printf(...)` → `fmt.Fprintf(a.out, ...)`; the tmux-missing warning and "Next:" lines also go to `a.out`. Change the suggestion text `mir-agent pair-dev` → `mir pair-dev`. `return nil` at end.
- **`cmdPairDev`**: `fatal` → `return`, `fmt.Printf("pinned owner %s\n", ...)` → `fmt.Fprintf(a.out, ...)`, `return nil`.
- **`cmdUp`**: convert `fatal`→`return`; the startup line `fmt.Printf("mir-agent up: machine %s ...")` → `fmt.Fprintf(a.out, "%s up: machine %s, signaling %s\n", a.binary, cfg.MachineID, cfg.SignalURL)`; the notice line → `selfupdate.New(repoSlug, a.binary).MaybeNotify(a.errOut, updateCachePath(*dir), version.Version, 24*time.Hour)`; the auto-update goroutine `go autoUpdateLoop(ctx, rt)` → `go a.autoUpdateLoop(ctx, rt)`. Keep `rt.Logf` writing to `os.Stderr` as in the source. End with `return nil` (note: `rt.Up(ctx)` returning non-nil with `ctx.Err()==nil` becomes `return err`).
- **`autoUpdateLoop`**: make it a method `func (a *app) autoUpdateLoop(ctx context.Context, rt *agent.Runtime)`; replace `selfupdate.New(repoSlug, "mir-agent")` → `selfupdate.New(repoSlug, a.binary)` and the two `fmt.Fprintf(os.Stderr, ...)` → `fmt.Fprintf(a.errOut, ...)`. Logic otherwise unchanged.

Worked example — `cmdPairDev`:

```go
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
```

- [ ] **Step 4: Wire the dispatch cases**

In `go/internal/cli/cli.go`, add above `default`:

```go
	case "enroll":
		return a.exit(a.cmdEnroll(argv[1:]))
	case "pair-dev":
		return a.exit(a.cmdPairDev(argv[1:]))
	case "up":
		return a.exit(a.cmdUp(argv[1:]))
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/agent_cmds.go go/internal/cli/cli.go go/internal/cli/cli_test.go && git commit -m "feat(cli): move agent commands (enroll/pair-dev/up + auto-update loop)"
```

---

## Task 5: Unify `pair` — resolve the responder/initiator collision

**Files:**
- Create: `go/internal/cli/pair.go`, `go/internal/cli/pair_test.go`
- Modify: `go/internal/cli/cli.go` (wire `pair`)
- Source: `go/cmd/mir/main.go` `cmdPair` 141-176 (initiator), `go/cmd/mir-agent/main.go` `cmdPair` 126-169 (responder)

- [ ] **Step 1: Write the failing test for `classifyPair`**

Create `go/internal/cli/pair_test.go`:

```go
package cli

import "testing"

func TestClassifyPair(t *testing.T) {
	if m, code, err := classifyPair(nil); err != nil || m != pairResponder || code != "" {
		t.Fatalf("no args = %v,%q,%v; want responder", m, code, err)
	}
	if m, code, err := classifyPair([]string{"ABC123"}); err != nil || m != pairInitiator || code != "ABC123" {
		t.Fatalf("one arg = %v,%q,%v; want initiator ABC123", m, code, err)
	}
	if _, _, err := classifyPair([]string{"a", "b"}); err == nil {
		t.Fatal("two args should error")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run ClassifyPair`
Expected: FAIL — `undefined: classifyPair`.

- [ ] **Step 3: Implement `pair.go`**

Create `go/internal/cli/pair.go`. `classifyPair` is the pure decision; `cmdPair` parses the union flagset, classifies on the leftover positionals, and runs the matching body (moved from the two old handlers). Imports: `context`, `encoding/hex`, `flag`, `fmt`, `os`, `strings`, `time`, `github.com/mdp/qrterminal/v3`, and `internal/{agent,client,defaults,pairing,sas}`.

```go
package cli

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/srcful/terminal-relay/go/internal/agent"
	"github.com/srcful/terminal-relay/go/internal/client"
	"github.com/srcful/terminal-relay/go/internal/defaults"
	"github.com/srcful/terminal-relay/go/internal/pairing"
	"github.com/srcful/terminal-relay/go/internal/sas"
)

type pairMode int

const (
	pairResponder pairMode = iota // no code: make THIS machine pairable (was `mir-agent pair`)
	pairInitiator                 // a code: pair TO the machine that printed it (was `mir pair <code>`)
)

// classifyPair decides direction from the positional args left after flag
// parsing: none = responder, one = initiator with that code, more = error.
func classifyPair(positionals []string) (pairMode, string, error) {
	switch len(positionals) {
	case 0:
		return pairResponder, "", nil
	case 1:
		return pairInitiator, positionals[0], nil
	default:
		return 0, "", fmt.Errorf("usage: mir pair [<code>]  (no code = make this machine pairable; <code> = pair to it)")
	}
}

func (a *app) cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "config directory")
	name := fs.String("name", hostname(), "machine display name (responder)")
	signalURL := fs.String("signal", defaults.SignalURL(), "signaling server base URL (responder)")
	webURL := fs.String("web", defaults.WebURL(), "browser SPA base URL the QR opens (responder)")
	_ = fs.Parse(args)

	mode, code, err := classifyPair(fs.Args())
	if err != nil {
		return err
	}
	if mode == pairInitiator {
		return a.pairInitiate(*dir, code)
	}
	return a.pairRespond(*dir, *name, *signalURL, *webURL)
}

// pairInitiate is the body of the old client cmdPair (cmd/mir/main.go:141-176).
func (a *app) pairInitiate(dir, codeStr string) error {
	signalURL, token, err := pairing.DecodeCode(codeStr)
	if err != nil {
		return err
	}
	idn, err := client.LoadOrCreateIdentity(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return err
	}
	defer closeConn()
	info, binding, err := pairing.RunInitiator(ctx, mc, token, idn.OwnerPub())
	if err != nil {
		return err
	}
	m := client.Machine{Name: info.Name, MachineID: info.MachineID, HostPubHex: info.HostPubHex, SignalURL: signalURL}
	if err := client.AddMachine(dir, m); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired machine %q — try: mir attach %s\n", m.Name, m.Name)
	fmt.Fprintf(a.out, "  safety number: %s  (must match the machine's)\n", sas.FromBinding(binding))
	return nil
}

// pairRespond is the body of the old agent cmdPair (cmd/mir-agent/main.go:126-169).
func (a *app) pairRespond(dir, name, signalURL, webURL string) error {
	cfg, err := agent.LoadOrInit(dir, name, signalURL)
	if err != nil {
		return err
	}
	token := pairing.NewToken()
	code := pairing.EncodeCode(signalURL, token)
	pairURL := strings.TrimRight(webURL, "/") + "/#" + code

	fmt.Fprintln(a.out, "Pair this machine:")
	fmt.Fprint(a.out, "\n  📱 Scan with your phone's camera — it opens the app ready to pair:\n\n")
	qrterminal.GenerateHalfBlock(pairURL, qrterminal.L, a.out)
	fmt.Fprintf(a.out, "\n  …or open: %s\n", pairURL)
	fmt.Fprintf(a.out, "  …or on the CLI:  mir pair %s\n", code)
	fmt.Fprintf(a.out, "\nwaiting for pairing (5 min)…\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mc, closeConn, err := pairing.DialPair(ctx, signalURL, pairing.RoomID(token))
	if err != nil {
		return err
	}
	defer closeConn()

	info := pairing.AgentInfo{HostPubHex: cfg.HostPubHex, MachineID: cfg.MachineID, Name: cfg.MachineName}
	ownerPub, binding, err := pairing.RunResponder(ctx, mc, token, info)
	if err != nil {
		return err
	}
	ownerHex := hex.EncodeToString(ownerPub)
	if err := agent.PinOwner(dir, ownerHex); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "✓ paired — trusting owner %s…\n", ownerHex[:16])
	fmt.Fprintf(a.out, "  safety number: %s  (must match the client's)\n", sas.FromBinding(binding))
	return nil
}
```

- [ ] **Step 4: Wire the dispatch case**

In `go/internal/cli/cli.go`, add above `default`:

```go
	case "pair":
		return a.exit(a.cmdPair(argv[1:]))
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/pair.go go/internal/cli/pair_test.go go/internal/cli/cli.go && git commit -m "feat(cli): unify pair (no-arg responder vs coded initiator)"
```

---

## Task 6: Reduce `cmd/mir/main.go` to a thin wrapper

**Files:**
- Replace: `go/cmd/mir/main.go`

- [ ] **Step 1: Replace the file**

Replace the entire contents of `go/cmd/mir/main.go` with:

```go
// go/cmd/mir/main.go — the mir node. All logic lives in internal/cli so the
// deprecated mir-agent shim can share it verbatim.
package main

import (
	"os"

	"github.com/srcful/terminal-relay/go/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
```

- [ ] **Step 2: Build to verify it compiles**

Run: `cd /Users/fredde/repositories/miranda/go && go build ./cmd/mir/`
Expected: builds clean, no unused-import errors.

- [ ] **Step 3: Smoke-test the binary**

Run: `cd /Users/fredde/repositories/miranda/go && go run ./cmd/mir --version`
Expected: prints `mir <version>` (e.g. `mir dev (none, unknown)`), exit 0.

Run: `cd /Users/fredde/repositories/miranda/go && go run ./cmd/mir bogus; echo "exit=$?"`
Expected: usage line on stderr, `exit=2`.

- [ ] **Step 4: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/cmd/mir/main.go && git commit -m "refactor(mir): thin main over internal/cli"
```

---

## Task 7: `mir-agent` deprecation shim

**Files:**
- Modify: `go/internal/cli/cli.go` (add `RunAgentCompat`)
- Replace: `go/cmd/mir-agent/main.go`
- Test: `go/internal/cli/cli_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/cli_test.go`:

```go
func TestRunAgentCompatWarnsAndForwards(t *testing.T) {
	var out, errb bytes.Buffer
	if code := RunAgentCompat([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(strings.ToLower(errb.String()), "deprecated") {
		t.Fatalf("stderr = %q, want deprecation notice", errb.String())
	}
	if !strings.HasPrefix(out.String(), "mir-agent ") {
		t.Fatalf("stdout = %q, want mir-agent version label", out.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ -run AgentCompat`
Expected: FAIL — `undefined: RunAgentCompat`.

- [ ] **Step 3: Add `RunAgentCompat` to `cli.go`**

Append to `go/internal/cli/cli.go`:

```go
const agentDeprecationNotice = "note: `mir-agent` is deprecated and now an alias for `mir` — use `mir up` / `mir pair` / `mir enroll`. This shim will be removed in a future release."

// RunAgentCompat is the deprecated mir-agent entry point: it prints a one-line
// deprecation notice to stderr, then dispatches exactly like Run but labelled
// "mir-agent" (so self-update fetches the mir-agent asset and notices read right).
func RunAgentCompat(argv []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, agentDeprecationNotice)
	return (&app{out: stdout, errOut: stderr, binary: "mir-agent"}).run(argv)
}
```

- [ ] **Step 4: Replace `cmd/mir-agent/main.go`**

Replace the entire contents of `go/cmd/mir-agent/main.go` with:

```go
// go/cmd/mir-agent/main.go — DEPRECATED shim. mir-agent is now an alias for mir;
// it forwards to the shared internal/cli with a deprecation notice. Kept so
// existing installs / systemd units keep working through the deprecation window.
package main

import (
	"os"

	"github.com/srcful/terminal-relay/go/internal/cli"
)

func main() { os.Exit(cli.RunAgentCompat(os.Args[1:], os.Stdout, os.Stderr)) }
```

- [ ] **Step 5: Run tests + build both binaries**

Run: `cd /Users/fredde/repositories/miranda/go && go test ./internal/cli/ && go build ./cmd/mir/ ./cmd/mir-agent/`
Expected: tests PASS; both binaries build.

- [ ] **Step 6: Smoke-test the shim**

Run: `cd /Users/fredde/repositories/miranda/go && go run ./cmd/mir-agent --version`
Expected: deprecation notice on stderr, `mir-agent <version>` on stdout.

- [ ] **Step 7: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add go/internal/cli/cli.go go/internal/cli/cli_test.go go/cmd/mir-agent/main.go && git commit -m "feat(mir-agent): deprecate to a thin alias shim over internal/cli"
```

---

## Task 8: Usage text, README, full build/test, and final verification

**Files:**
- Modify: `go/internal/cli/cli.go` (usage already lists all subcommands — verify)
- Modify: `README.md`

- [ ] **Step 1: Verify the usage string lists every subcommand**

Confirm `usage()` in `go/internal/cli/cli.go` reads:
`usage: mir <up|attach|list|pair|enroll|pair-dev|keygen|add-machine|run|self-update|--version> [flags]`
(No code change if already correct from Task 1.)

- [ ] **Step 2: Update the README Quickstart to the unified commands**

In `README.md`, the Quickstart block currently uses `mir-agent pair` / `mir-agent up`. Change the agent lines to the unified binary, and add a one-line deprecation note. Replace:

```bash
# on a machine you want to reach:
mir-agent pair               # prints a pairing code + QR, then waits
mir-agent up &               # run the agent (persistent tmux sessions)
```

with:

```bash
# on a machine you want to reach (same `mir` binary — every node is symmetric):
mir pair                     # prints a pairing code + QR, then waits
mir up &                     # serve this machine (persistent tmux sessions)
```

And under "Updating", change `mir-agent self-update` → `mir self-update` and add: "`mir-agent` is a deprecated alias for `mir` and forwards to it." (Keep `mir-agent up --auto-update` mention but note it equals `mir up --auto-update`.)

- [ ] **Step 3: Full module build + test (race)**

Run: `cd /Users/fredde/repositories/miranda/go && go build ./... && go test ./...`
Expected: everything builds; all tests pass (existing `internal/{client,agent,signal,...}` suites + the new `internal/cli` suite).

Run: `cd /Users/fredde/repositories/miranda/go && go test -race ./internal/cli/ ./internal/agent/`
Expected: race-clean.

- [ ] **Step 4: Verify `make install` still builds all three binaries**

Run: `cd /Users/fredde/repositories/miranda && make install`
Expected: `~/.local/bin/{mir,mir-agent,mir-signal}` built. Then:

Run: `~/.local/bin/mir --version && ~/.local/bin/mir-agent --version 2>&1 | head -2`
Expected: `mir <version>`; deprecation notice + `mir-agent <version>`.

- [ ] **Step 5: Confirm no dangling references to the old split**

Run: `cd /Users/fredde/repositories/miranda && grep -rn "mir-agent pair-dev\|mir-agent up\|mir-agent pair\b" --include=*.go --include=*.md . | grep -v docs/superpowers`
Expected: no hits in code or top-level docs (the deprecation shim and plan/spec docs may still reference the name intentionally — those are fine).

- [ ] **Step 6: Commit**

```bash
cd /Users/fredde/repositories/miranda && git add README.md go/internal/cli/cli.go && git commit -m "docs: unified mir node commands; mir-agent deprecated"
```

---

## Self-Review (run before handing off / opening the PR)

- [ ] **Spec coverage:** §2 "one binary, symmetric node" → Tasks 1-7 (all subcommands under `mir`). `mir-signal` stays separate → untouched (verify it still builds in Task 8 Step 3). `mir pair [<code>]` arg-dispatch → Task 5. `mir-agent` shim + deprecation window → Task 7. Back-compat config dir `~/.terminal-relay` kept → `defaultDir` unchanged (Task 2). The tmux "run as your user" note is documentation about runtime behavior, not a code change — out of scope for A1 (tracked by issue #21); no task needed.
- [ ] **Placeholder scan:** none — every step shows real code or an exact command. The "move" steps give source coordinates + the fixed transformation list + a worked example.
- [ ] **Type/name consistency:** handler methods are `func (a *app) cmdX(args []string) error` everywhere; dispatch calls match (`cmdKeygen`, `cmdAddMachine`, `cmdList`, `cmdAttach`, `cmdRun`, `cmdSelfUpdate`, `cmdEnroll`, `cmdPairDev`, `cmdUp`, `cmdPair`). `app` fields `out`/`errOut`/`binary` used consistently. `classifyPair`/`pairMode`/`pairResponder`/`pairInitiator` consistent between `pair.go` and `pair_test.go`.
- [ ] **Behavior preservation:** defaults (`~/.terminal-relay`, flag names/defaults, exit codes 1/2), update-notice/auto-update logic, and all `internal/*` calls are unchanged; only the cmd layer is restructured. Existing e2e suites in `internal/client`/`internal/agent` are the regression net and must stay green (Task 8 Step 3).

---

## Execution notes

- After all tasks + self-review pass, open a focused PR (squash-merge per repo convention). Branch: `unified-node-binary`.
- Optional deeper check before merge: `cd deploy/netsim && ./run.sh` (real NAT traversal still works end-to-end with the merged binary).

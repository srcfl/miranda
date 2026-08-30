# Engine seam — carry herdr sessions, not only tmux

> Issue #107. Investigation + design; no build. The bar is the G1 spec: every
> claim about herdr below was produced by running herdr 0.8.2 in a contained
> sandbox and reading what came back. Where a thing was not measured, it says
> so.

## One sentence

Miranda's agent side has eight tmux-shaped couplings, not one; this spec names
each one's engine-neutral capability, reports what herdr can and cannot do for
it, and specifies an `Engine` interface plus a capability table that makes
`mir up --engine herdr` either work honestly or refuse out loud.

## Why herdr at all

herdr is a background server that owns terminal sessions, recognizes which
panes hold AI coding agents, tracks their state (idle / working / blocked /
done / unknown), and exposes all of it through a socket API. Its remote story
is SSH: `herdr --remote workbox` is a thin client over SSH, and the docs offer
nothing else — no relay, no browser, no phone, no sharing. That is the whole
argument. herdr owns the machine-local layer Miranda deliberately does not
build; Miranda owns the cross-device layer herdr deliberately does not build.
Its users are Miranda's users, already gathered.

The distribution play is that `mir up --engine herdr` is a one-line addition
to somebody else's existing setup, not a migration.

---

## Part 1 — what is actually coupled to tmux

Twenty-six production `exec` sites invoke `tmux`, across five files. The
couplings, and what each one *needs* said without naming a multiplexer:

### 1.1 Launch

`go/internal/cli/agent_cmds.go:79` — `--shell` defaults to the string
`"tmux:new:-A:-s:main"`, split on `:` at `agent_cmds.go:102` into argv, gated
by the tmux installer at `agent_cmds.go:107`, handed to `agent.NewRuntime` at
`agent_cmds.go:136`, stored as `Runtime.launch` (`agent/runtime.go:68`), spawned
per attach by `StartPTY` (`agent/pty.go:22`). `go/internal/cli/tmux_bootstrap.go`
is a whole file of package-manager knowledge (`apt-get`/`dnf`/`pacman`/`brew`),
with `fallbackLaunch = []string{"sh"}` at `tmux_bootstrap.go:53`.

**Needs:** start one persistent, reattachable terminal per attach; know whether
the engine exists here; offer to install it if not.

### 1.2 Snapshot → FrameWindows v2

`agent/windows.go:262` `tmuxSessionsJSON` runs exactly one listing command:

    tmux list-windows -a -F "#{session_name}|#{window_id}|#{window_index}|#{window_name}|#{window_active}|#{window_activity_flag}|#{window_bell_flag}|#{pane_current_command}|#{window_panes}"

plus `tmux list-clients -F …` (`windows.go:206`) to find *our* client by PID, and
`tmux list-sessions -F "#{session_name}|#{session_group}"` (`windows.go:173`) for
grouped aliases. The result marshals to `sessSnapshot` (`windows.go:251`) and
ships as frame type `0x04` (`noise/frame.go:16`, `web/src/noise/frame.js:6`):

    {"v":2,"sess":[{"n":"main","act":true,"aw":"@7",
      "win":[{"id":"@7","i":0,"n":"claude","cmd":"node","p":2,"a":false,"b":false}]}]}

**Needs:** a list of views, each with a name, a flag for "this viewer is here",
the id of its current window, and per window: a stable handle, an index, a
label, a foreground-command hint, a pane count, and activity/bell flags.

### 1.3 Control allow-list

`agent/windows.go:21-135` `runTmuxControl`, payload `{"a","s","t","n"}`. Ten
actions: `select-window`, `kill-window`, `rename-window`, `new-window`,
`next-window`, `previous-window`, `switch-session`, `new-session`,
`rename-session`, `kill-session`. Cross-view moves go through
`tmux switch-client -c <tty>` so they hit *this* client and no other
(`windows.go:41`, `:89`, `:104`, `:118`). `kill-session` is guarded by
`sameView` (`windows.go:131`) so a viewer cannot kill the view it sits in.
Frame type `0x05`; the only sender is the web strip (`web/src/app.js:918-926`)
— the Go CLI is a raw passthrough and sends none of these.

**Needs:** a per-viewer RPC to select / create / rename / close a window and
switch / create / rename / destroy a view, scoped to the caller's own client,
refusing to destroy the view the caller is inside.

### 1.4 R3 — the change push

`agent/tmuxhooks.go`. 22 hook names (`tmuxhooks.go:64-73`) are installed
globally at slot `[999]` with the body `wait-for -S mir-window-strip`
(`tmuxhooks.go:155`); the agent parks a blocking `tmux wait-for` process
(`tmuxhooks.go:218`) and re-arms every minute (`hookRearm`, `:43`).
Refcounted across concurrent attaches (`:112`, `:136`). Downstream,
`agent/windowpush.go:26` debounces 50 ms, polls 1 s as a safety net, and
dedupes on marshalled JSON.

This is a lot of machinery to buy one bit: *something changed*.

**Needs:** a change signal from the engine, cheap, without streaming pane
content, with a poll fallback.

### 1.5 D4 — per-attach isolation

`agent/grouped.go`. Each attach gets `mir-<8 hex>` (`grouped.go:46`) launched as
`tmux new-session -A -t main -s mir-xxxxxxxx` (`grouped.go:79`), so every viewer
has its own current window and its own size over one shared set of windows.
`ensureGroupedBase` (`:64`), `killGroupedSession` on detach (`runtime.go:553`),
`sweepOrphanGroupedSessions` on startup (`runtime.go:167`) with a guard that
never reaps the last member of a group (`grouped.go:120`), and
`collapseGrouped` (`windows.go:332`) which hides every `mir-*`/`guest-*` view
from the snapshot and folds this viewer's cursor onto the base entry.

The whole thing hangs off `isDefaultTmuxLaunch` (`grouped.go:26`), which is
byte-exact argv equality against `{"tmux","new","-A","-s","main"}`.

**Needs:** per-attach independent current-window cursors and sizes over one
shared window set; a per-attach view identity; cleanup on detach; orphan
reaping after a crash; and a way to hide engine objects Miranda minted.

### 1.6 G1 — guest sharing

`agent/guest.go`. Read-only (`serveGuestRO`, `guest.go:114`) is deliberately
*not* a tmux client: one `tmux capture-pane -e -p -t <scope>` for the initial
paint (`guest.go:129`), then a FIFO the agent creates (`guest.go:145`) fed by
`tmux pipe-pane -O -t <scope> "cat > <fifo>"` (`guest.go:148`), teardown by a
bare `pipe-pane` (`guest.go:151`). Guest input is decoded, counted, dropped
(`guest.go:180-198`); `FrameControl` never reaches the engine (`guest.go:179`).
That shape exists because the G1 experiments found `tmux attach -r` clients can
still `switch-client` server-wide. Read-write (`serveGuestRW`, `guest.go:222`)
is a `guest-<gid[:8]>` grouped session and is refused outright on a non-default
launch (`guest.go:223`).

**Needs:** (a) one view's current contents once, with colour, plus everything
that view subsequently emits, output only, with no interactive client
anywhere; (b) a named, revocable, writable secondary attachment onto an
existing view.

### 1.7 `mir doctor`

Two checks, both presence-only: `exec.LookPath("tmux")` → warn or ok
(`cli/doctor.go:69-73`), and `tmux -V` printed into the `--share` bug-report
header (`doctor.go:288-297`). No minimum version is asserted anywhere.

**Needs:** is an engine available here, which one, which version, and what can
it do.

### 1.8 FrameWindows consumers

- Go CLI overview: `cli/overview_model.go:240` `windowsSummary` reads only
  `sess[].act` and `sess[].win[].n` and renders
  `"3 windows — claude, build, logs"`. Plain `mir attach` discards the frame
  entirely (`client/bridge.go:26`, `client/term.go:52`).
- Web: `web/src/app.js:932` `sessionsView()` (tolerates v1), `:939` `hasAlert`,
  `:991` `renderStrip` (uses `id`, `i`, `n`, `cmd`, `a`, `b`), `:1028`
  `openGrid` (adds `p`), `:1060` `openSessions`. The guest client
  (`web/src/guest.js`) never reads it.

**Needs:** one push-updated view/window list with stable handles; no consumer
needs a tmux-shaped identifier, only a stable opaque one.

### 1.9 Assumptions outside the eight

`client/mux.go:227` sends a resize twice on focus change because tmux only
repaints on a real size change. `deploy/launchd/install.sh:41` resolves
`dirname $(command -v tmux)` and prepends it to the daemon's PATH.
`netsim/Dockerfile:20` installs tmux; `netsim/docker-compose.yml:166` passes
`--shell=tmux:new:-A:-s:main`. Several tests skip without a real tmux binary
(`agent/grouped_test.go:17` pins the default argv byte-for-byte).

---

## Part 2 — herdr, measured

**Version tested: herdr 0.8.2** (stable channel, macos/aarch64), downloaded
2026-08-30 by `https://herdr.dev/install.sh` with `HERDR_INSTALL_DIR` pointed at
a scratch directory. The installer writes exactly one file, verifies a SHA-256
from `https://herdr.dev/latest.json`, and touches no shell config. Every
experiment ran against a private server under a throwaway `HOME`; the
machine's own herdr config was never opened.

### 2.0 Shape of the thing

`herdr` with no arguments launches or attaches a full-screen TUI — sidebar,
tab bar, panes — against a background server. `herdr server` runs that server
headless, which is what a Miranda agent would do. The nouns are
**workspace ≈ tmux session**, **tab ≈ tmux window**, **pane ≈ tmux pane**, and
a **session** is a whole separate server with its own socket
(`~/.config/herdr/sessions/<name>/herdr.sock`). Public ids are opaque and
stable: `w1`, `w1:t1`, `w1:p1`.

The socket API is newline-delimited JSON over a unix socket. `herdr api
schema --json` printed a 255 KB JSON Schema covering 90 request methods, 26
event kinds, and 3 subscription kinds — a real contract, versioned by a
`protocol` integer (20 in 0.8.2).

> **Gotcha worth writing down now:** the socket path is
> `$HOME/.config/herdr/herdr.sock`, and a long `HOME` overflows `sun_path`.
> The first sandbox run failed with `local socket name length exceeds capacity
> of sun_path of sockaddr_un`. A Miranda daemon running under an unusual HOME
> will hit this; `HERDR_SOCKET_PATH` is the escape hatch.

### 2.1 Attaching a PTY client — works, and it is the whole UI

Ran `herdr` on a pty at 100×30, typed `echo HELLO_FROM_A`, captured 13.8 KB.
The TUI painted, the command ran, the output came back. So the launch seam is
trivial: `--shell herdr` already works today, in the sense that a browser would
see herdr's own interface — sidebar and tab bar included.

That is the first honest consequence. On tmux, Miranda hides the engine and
renders its own window strip from FrameWindows. On herdr, the engine draws its
own chrome inside the pane. Two window strips, one of them ours.

### 2.2 Two clients share one focus — D4 is unsupported

The decisive experiment. Two markers, one per tab. Client A attached at
100×30 and sat still. Client B attached at 90×24 and pressed `ctrl+b n`
(`next_tab`).

- Server snapshot before: `w1:t1 focused:true`, `w1:t2 focused:false`.
- After B's keypress: `w1:t1 focused:false`, `w1:t2 focused:true`.
- **Client A's own byte stream then contained `TAB_TWO_MARKER`.** Replaying A's
  capture through a terminal emulator shows A's screen rendering tab 2's pane.
  A followed B.

Repeated one level up with two workspaces: A attached, B opened the workspace
picker and moved to `w2`; afterwards both A and B had `WS_TWO_MARKER` and
neither ended on `WS_ONE_MARKER`. Focus in herdr 0.8.2 is a property of the
server, not of a client.

There is no grouped-session analogue. Named sessions are separate servers with
separate panes, so they do not share windows. `agent.view.set` sounds like a
per-client view but the docs describe a sidebar projection over the Agents
list, not a viewport. **Verdict: two Miranda attaches onto one herdr session
will fight over the current tab exactly the way `tmux new -A -s main` did
before D4.**

### 2.3 The per-pane streams — this is the good part

`herdr terminal session observe <pane>` and `herdr terminal session control
<pane>` are documented, in herdr's own words, "for third-party bridges that
only need rendered terminal bytes". They print newline-delimited JSON on plain
stdout; no pty required. Frame records look like:

    {"type":"terminal.frame","seq":1,"full":true,"encoding":"ansi",
     "width":100,"height":30,"bytes":"<base64 ANSI>"}

`full:true` is the initial repaint; later frames are deltas. The stream ends
with `{"type":"terminal.closed","reason":"…"}`.

**Observe is confined, and I checked rather than assumed.** With an observer
running on `w1:p1`:

- I sent it `terminal.input` with `echo WROTE_FROM_RO\n`, then a
  `terminal.resize`, then a `terminal.scroll`. Reading the pane afterwards:
  `WROTE_FROM_RO` occurrences — **0**. The pane's own size did not change.
- While the observer ran, I focused tab 2 and printed
  `TAB_TWO_MARKER_SECRET` in a pane the observer was not watching. The
  observer's decoded byte stream: `TAB_TWO_MARKER_SECRET` — **0 hits**; and
  `RO_PROBE_PANE1_AGAIN`, printed in *its* pane after the focus moved away —
  **2 hits**. It stayed pinned to its pane and kept receiving.

This is a cleaner read-only primitive than G1's tmux mirror. No hook, no FIFO,
no `pipe-pane` teardown, no `capture-pane` for the first paint — one stream
gives both, and confinement is structural because no interactive client exists.
Multiple observers are allowed.

**Control is exclusive, with an explicit takeover.** A second controller on the
same terminal without `--takeover`:

    {"type":"terminal.closed",
     "reason":"terminal attach failed: terminal term_… already has an attached
               client; retry with --takeover"}

With `--takeover`, the incumbent received
`{"type":"terminal.closed","reason":"terminal attach taken over"}` and the new
controller took input. `terminal.input` from a controller does land: `C1_WROTE`
and `C3_WROTE` both appear in the pane.

**Sizing has two regimes, and the difference matters.** With no full-UI client
attached, a controller's `--cols/--rows` resizes the real pty — measured
`SIZE_IS 200x50`, then `40x12`, then `120x40` from `tput` inside the pane, and
two controllers on two different panes held 111×33 and 55×15 at the same time.
But with a full-UI client attached at 140×44, a controller asking for 40×12
left the pane at `113x43` before and after. An observer never resizes anything:
during a 30×8 observe the pane reported `AFTER_OBS 120x40`.

So a small viewport is a **crop, not a reflow** whenever anyone else owns the
geometry. Rendering the 40×12 observer frame confirms it — every line is cut at
column 40, not wrapped. A phone attaching to a session a desktop client also
holds sees the left-hand slice of a 113-column pane. tmux's grouped sessions do
not have this problem; each client gets its own size and tmux reflows.

### 2.4 Push — herdr does what R3 built 250 lines of hooks to fake

`events.subscribe` over the socket, then the connection stays open. On
subscribe herdr replays current state as a burst of events, then pushes live
ones. Measured, in order, from a single subscription:

    +0.11s tab_created / tab_focused / tab_renamed / pane_created /
           pane_focused / layout_updated / workspace_focused
    +3.75s pane.agent_status_changed {"agent":"claude","agent_status":"working",
                                      "pane_id":"w1:p1","workspace_id":"w1"}
    +4.47s pane.agent_status_changed {"agent":"claude","agent_status":"blocked", …}

26 event kinds exist, including `pane_exited`, `pane_agent_detected` and
`layout_updated` (which carries the full split tree for a tab). Scoped kinds
(`pane.agent_status_changed`, `pane.output_matched`, `pane.scroll_changed`)
require a `pane_id`; subscribing without one is rejected with
`invalid_request: missing field 'pane_id'`, which is how I learned it.

No hooks to install, no global server option to mutate, no refcounting, no
re-arm timer. R3's whole apparatus collapses to one open socket.

### 2.5 Snapshot — a better shape than ours

`herdr api snapshot` (`session.snapshot`) returns workspaces, tabs, panes,
per-tab layout snapshots and agents in one document. Every workspace, tab and
pane record carries `agent_status`, rolled up: a blocked pane makes its tab and
its workspace look blocked.

    {"workspace_id":"w1","label":"demo","active_tab_id":"w1:t1",
     "focused":true,"agent_status":"idle","tab_count":1,"pane_count":1}
    {"tab_id":"w1:t1","label":"1","number":1,"focused":true,
     "pane_count":2,"agent_status":"unknown"}
    {"pane_id":"w1:p1","tab_id":"w1:t1","cwd":"…","terminal_id":"term_…",
     "focused":true,"agent_status":"unknown","scroll":{…}}

Every field FrameWindows v2 carries has a herdr equivalent except two:
`winInfo.b` (bell) and `winInfo.a` (activity) have no direct counterpart —
`pane_output_changed` and `pane.scroll_changed` are the nearest live signals,
and mapping them to a sticky per-window flag would be Miranda's own
bookkeeping. `winInfo.cmd` maps to the pane's foreground process, which
`pane process-info` exposes but the snapshot does not inline.

### 2.6 Agent state — the upside, with an asterisk

I drove state through `pane report-agent`, which is the same path an
integration uses:

    herdr pane report-agent w1:p1 --source probe --agent claude --state blocked \
      --message "needs approval"

`herdr agent list` then returned
`{"agent":"claude","agent_status":"blocked","pane_id":"w1:p1","state_change_seq":2,…}`
and the change arrived as a pushed event within a tick.

The asterisk, from herdr's own docs: for Claude Code, Codex, Cursor, Copilot,
Droid and most of the list, the state authority is a **screen manifest** —
herdr scrapes the bottom of the pane buffer and matches TOML rules. Lifecycle
hooks are authoritative only for Pi, OMP, Kimi, OpenCode, Kilo and MastraCode.
The manifests update themselves from herdr.dev in the background
(`[update] manifest_check = true` by default, alongside `version_check`).

So "blocked" is a third party's heuristic over a screenshot, refreshed over the
network, not ground truth. Miranda may show it. Miranda must not promise it,
and must decide whether an agent process that phones herdr.dev on a schedule is
acceptable inside a product whose pitch is a relay that cannot read anything.

### 2.7 What I did not verify

- **Linux behaviour.** Everything above is macOS/aarch64. The socket path
  limit, the process-detection mode (`HERDR_PROCESS_DETECTION=child-groups` is
  documented for restricted Linux runtimes), and the pty resize regimes should
  be re-measured on Linux before shipping.
- **Real agent detection.** I injected state through `report-agent`; I never
  ran Claude Code or Codex inside a herdr pane and watched the screen-manifest
  classifier work. Its accuracy is unmeasured here.
- **Reconnect behaviour under packet loss.** Netsim has no herdr scenario.
  Whether a `terminal session control` stream survives a WebRTC flap the way a
  tmux client does is unknown.
- **Long-run stability of a control stream** — the longest I held one was 15
  seconds.
- **Whether `terminal_id` is stable across a server restart.** The docs say
  panes are restored; they do not say ids are preserved.
- **Windows.** Out of scope, and direct terminal attach is Unix-only there
  anyway.

---

## Part 3 — the seam

### 3.1 The interface

The smallest surface that satisfies Part 1. It lives in
`go/internal/agent/engine` and nothing above it names a multiplexer.

```go
// Engine is one machine-local terminal engine. One implementation per engine.
type Engine interface {
    // Identity and diagnosis.
    Name() string                       // "tmux" | "herdr"
    Probe(ctx) (Info, error)            // installed? version? server reachable?
    Caps() Caps                         // static per-engine capability set

    // Launch: argv for one attach's PTY. attachID is Miranda's, opaque here.
    Launch(ctx, attachID string) (argv []string, cleanup func(), err error)

    // Snapshot: engine-neutral view/window tree for this attach.
    Snapshot(ctx, view ViewRef) (*Tree, error)

    // Change signal: closed or fed by the engine; Miranda debounces.
    Watch(ctx, view ViewRef) (<-chan struct{}, error)

    // Control: one verb per FrameControl action, scoped to this attach.
    Control(ctx, view ViewRef, cmd Command) error

    // Mirror: confined, output-only stream of one window. Nil if unsupported.
    Mirror(ctx, target WindowRef, size Size) (io.ReadCloser, error)

    // GuestWrite: a named, revocable writable attachment. Nil if unsupported.
    GuestWrite(ctx, scope string, gid string) (argv []string, cleanup func(), err error)

    // Sweep: reap this engine's orphaned Miranda-minted objects at startup.
    Sweep(ctx) error
}
```

`ViewRef` is an opaque engine string (`"mir-3f2a91cc"` for tmux, `"w1"` for
herdr). `WindowRef` likewise (`"@7"` / `"w1:p1"`). `Tree` is the neutral form
of §1.2. `Command` is the ten-verb enum from §1.3 plus its validated arguments —
validation moves out of `runTmuxControl` and into the neutral layer, so the
`winIDRe`/`safeName`/`validSessTarget` rules stop being tmux-shaped.

`Caps` is the honesty mechanism:

```go
type Caps struct {
    PerAttachView   bool // two attaches, two current windows
    PerAttachSize   bool // two attaches, two sizes, engine reflows
    ConfinedMirror  bool // output-only stream that cannot see other views
    GuestWrite      bool // named revocable writable attachment
    PushEvents      bool // change notification without polling
    Control         ControlSet // which of the ten verbs exist
    AgentState      bool // engine reports agent lifecycle state
}
```

Two things stay out of the interface on purpose. Install/bootstrap is not an
`Engine` method — it is a per-engine `Bootstrap` in the CLI layer, because it
runs before an engine exists. And nothing about grouped-session naming leaks
out; `Launch` returning `(argv, cleanup)` is the whole contract, and the tmux
implementation hides `mir-<hex>` behind it.

### 3.2 Capability table

| Capability | tmux 3.7b | herdr 0.8.2 | Miranda's behaviour on herdr |
|---|---|---|---|
| Launch a persistent, reattachable terminal | supported | supported (`herdr`, server auto-starts) | full |
| Snapshot of views/windows | supported (`list-windows -a -F`) | supported, richer (`session.snapshot`) | full |
| Push on change | degraded — 22 global hooks + `wait-for` + 1 s poll | supported (`events.subscribe`, ~0 ms, no server mutation) | full, and simpler |
| Control: select / new / rename / close window | supported | supported (`tab.focus/create/rename/close`) | full |
| Control: switch / new / rename / kill view | supported, scoped to *this* client via `switch-client -c <tty>` | **degraded** — `workspace.focus` is server-global; every viewer moves | refuse the four view verbs; the strip hides them and says why |
| **Per-attach current window (D4)** | supported (grouped sessions) | **unsupported** — measured: A followed B's tab and workspace switch | **refuse a second concurrent attach** (§3.4) |
| Per-attach size | supported (grouped + `ignore-size`) | **degraded** — a controller sizes the pty only when no UI client owns geometry; otherwise the smaller viewport is a crop, not a reflow | allow, warn once in `doctor` and on second attach |
| Confined read-only mirror (G1 ro) | supported, but only as a non-client mirror (`-r` clients leak via `switch-client`) | **supported, better** — `terminal session observe`; input/resize/scroll measurably ignored; measurably pinned to its pane | full |
| Writable guest (G1 rw) | supported (`guest-*` grouped session) | **degraded** — `terminal session control` is one pane, exclusive, and `--takeover` lets anyone evict anyone | ship as pane-scoped only, or hold; see open question 3 |
| Agent state (idle/working/blocked) | unsupported | supported, heuristic for most agents | show it, labelled as the engine's opinion |
| Bell / activity per window | supported (`window_bell_flag`, `window_activity_flag`) | **unsupported** as a sticky flag; `pane_output_changed` is the nearest live signal | omit `a`/`b`; the strip degrades to no alert dots |
| Foreground command per window | supported (`pane_current_command`) | degraded — `pane process-info` per pane, not in the snapshot | fill `cmd` from a second call, or leave empty |

### 3.3 The rule: no silent degradation

Three enforcement points, in order of how loud they are.

1. **`mir doctor` reports engine capabilities.** Today it prints "tmux is
   available". It grows a block per detected engine: name, version, server
   reachable, and the `Caps` set rendered as prose — "herdr 0.8.2: one viewer at
   a time; read-only sharing available; window alerts unavailable".
2. **`mir up --engine herdr` prints what it gives up, once, at startup**, and
   writes it into HELLO so the client knows before the user does.
3. **The refusal.** A capability a feature needs and the engine lacks is an
   error at the point of use with the engine named in it, never a quiet
   no-op. `mir share --write` on an engine without `GuestWrite` says so and
   exits non-zero. `runControl` refuses a view verb the engine cannot scope,
   and the frame is answered with an error the strip renders.

Only one case is a hard refusal rather than a warning, and it is the one that
would otherwise corrupt somebody's working session.

### 3.4 The second-attach problem

On tmux, the second attach is free — grouped sessions were built for it. On
herdr it is destructive: the phone's tab switch yanks the desktop's screen. So:

**On an engine with `PerAttachView == false`, Miranda serves one interactive
attach per machine at a time.** A second attach is refused with the machine
name, the engine name, and one sentence of why, plus the two ways out: detach
the other client, or open read-only. A read-only guest is unaffected — observe
streams are unlimited and confined, so `mir share` (ro) works on herdr with no
caveat at all.

This is the honest reading of "no feature may silently degrade". Letting two
viewers fight is a worse product than refusing the second one, and it is
exactly the bug D4 was written to kill.

### 3.5 Choosing the engine

Precedence, highest first:

1. `mir up --engine <name>` — explicit, and an unknown name is an error listing
   the known ones.
2. `engine` in the agent config (`agent.Config`, `agent/store.go:17`) — new
   field, persisted, so a machine keeps its engine across restarts. `--shell` is
   *not* persisted today; `--engine` is, because it changes what the machine is.
3. Detection: tmux if present, else herdr if present, else the `sh` fallback.
   tmux wins ties because it is the only engine where every feature is
   supported.

`--shell` survives untouched and still means "run exactly this argv, no engine
integration" — it is the escape hatch, and it already disables the snapshot,
the push and the control channel through one predicate
(`sessionFromLaunch`, `runtime.go:559`). `--engine` and `--shell` together is an
error.

### 3.6 `mir up --engine herdr` on a machine without herdr

U2's pattern, adapted from `cli/tmux_bootstrap.go`:

- **TTY, interactive:** print what will be installed, from where, and to where
  (`https://herdr.dev/install.sh` → `~/.local/bin/herdr`, one binary, SHA-256
  verified against `latest.json`), then ask. Decline → fall back to tmux if
  present, else `sh`, and say which.
- **Non-TTY / `--yes` absent:** refuse with the exact command to run by hand.
  Never install silently into a daemon's environment.
- **Pinning:** Miranda should record the version it installed and refuse a
  manifest whose checksum does not match what it expected, rather than
  delegating trust to a shell pipe. The installer already verifies SHA-256
  against the manifest; Miranda adds nothing by re-running it, but it does
  add something by *recording* what it got.
- **PATH:** `deploy/launchd/install.sh:41` prepends tmux's directory to the
  daemon PATH. It must learn the same trick for the chosen engine, or the
  launchd agent will start and find nothing.
- **Config isolation:** set `HERDR_SOCKET_PATH` explicitly under the Miranda
  state dir rather than inheriting `$HOME/.config/herdr` — it avoids the
  `sun_path` overflow measured in §2.0 and keeps Miranda's server out of the
  user's own herdr session.

### 3.7 FrameWindows stays one wire format

The v2 shape does not change and the JS decoder does not branch on engine.
Rules:

- `sess[].n`, `sess[].aw`, `win[].id` become **opaque strings**. They already
  are on the wire; only the agent's regexes assume `@N` (`winIDRe`,
  `windows.go:12`). Those regexes move behind the engine, so herdr can put
  `w1:t1` in `id` and nothing above notices. Web already treats `w.id` as a
  token to echo back in a control payload.
- `win[].i` (index) is `number` on herdr, `window_index` on tmux. Same meaning.
- `win[].a` / `win[].b` become **optional**. Both consumers already tolerate
  falsey (`app.js:939` `hasAlert`); on herdr they are simply absent and no
  alert dot renders. This is a degradation the user can see, which is the
  point.
- `win[].cmd` and `win[].p`: fill where cheap, omit where not.

**The v2 extension for agent state** is one optional field per window:

    "win":[{"id":"w1:t1","i":1,"n":"reviewer","st":"blocked"}]

`st` ∈ `idle|working|blocked|done|unknown`, absent on tmux. Adding an optional
field to a JSON object cannot break the v2 shape: the Go decoder ignores
unknown fields, the JS reads what it wants, and old clients see today's frame.
The overview line becomes `"3 windows — reviewer ⏸, build, logs"` and the web
strip gains a state dot. No frame-type change, no version bump, no new vector.

If a later engine needs *structured* agent data — session ids, messages, the
`explain` output — that is a new frame type, not a wider FrameWindows.

### 3.8 Slices

| # | Slice | Acceptance |
|---|---|---|
| E1 | Extract the seam with tmux as the only implementation: `engine.Engine`, `Caps`, neutral `Tree`/`Command`, tmux impl wrapping today's code verbatim. No behaviour change. | `go test ./...` and `npm test` green with zero test edits; `grep -rn '"tmux"' go/internal/agent \| grep -v engine/tmux` returns nothing |
| E2 | `--engine` flag, config field, detection precedence, `mir doctor` capability block. Still tmux-only. | `mir up --engine bogus` errors listing engines; `mir doctor` prints tmux's caps; `--engine` + `--shell` refused |
| E3 | herdr engine: `Probe`, `Launch`, `Snapshot` (from `session.snapshot`), `Watch` (from `events.subscribe`). Read-only path only — no control, no sharing. | Against a real herdr server: FrameWindows renders in the CLI overview and the web strip; a tab created in herdr appears in the strip in under 200 ms with no polling |
| E4 | herdr `Control`: the six window verbs. The four view verbs report unsupported. | Web strip select/new/rename/close work on herdr; switch-session shows a refusal with the engine named, not a dead button |
| E5 | Honest degradation: second-attach refusal on `PerAttachView == false`, the startup notice, HELLO carrying caps. | Two concurrent attaches to a herdr machine: the second is refused with a useful message; the first is undisturbed. Regression test asserts the first client's byte stream is unchanged |
| E6 | herdr `Mirror` via `terminal session observe`; `mir share` (ro) works on herdr. | Adversarial test mirroring G1c: the guest cannot inject (assert zero bytes reach the pane) and cannot see a second pane's output (assert a marker printed elsewhere never appears) |
| E7 | Agent state: optional `st` in FrameWindows v2, overview + strip rendering, `mir doctor` says where the state comes from. | Old client + new agent renders as today; new client + tmux renders as today; new client + herdr shows state. One vector added, none changed |
| E8 | Bootstrap: `mir up --engine herdr` install prompt, non-TTY refusal, launchd PATH, `HERDR_SOCKET_PATH` isolation, recorded version | Clean machine: prompt → install → up → attach. Non-TTY: refuses with the manual command |

Order E1 → E2 → E3 → E4 → E5 → E6 → E7 → E8. E1 is pure refactor and must
land alone. E5 gates every later slice, because until the refusal exists a
herdr machine can be corrupted by a second viewer.

### 3.9 Out of scope

- Rendering herdr's TUI chrome away, or asking Miranda to draw herdr's sidebar.
  On herdr the engine draws its own interface; Miranda's strip is a second one
  and we live with the duplication in v1.
- Building per-attach isolation *for* herdr (a proxy that fans one server into
  several apparent views). That is a multiplexer, and `docs/product.md:59`
  says we do not build one.
- Contributing per-client focus upstream to herdr. Worth doing; not this spec.
- Any third engine (Zellij, screen, WezTerm mux). The seam should make them
  cheap; adding them is not this issue.
- herdr's worktree, plugin, notification, graphics and popup surfaces. Miranda
  needs seven methods, not ninety.
- Netsim scenarios for herdr, and Linux verification of §2.7's gaps. Both are
  real work; both belong to E3's acceptance rather than this document.
- Changing the Noise framing, the relay, or anything the relay can see. An
  engine seam is entirely above the transport.

---

## Part 4 — the strategic note

**What Miranda gains.** A second engine proves the product claim is
"continuity", not "tmux with crypto" — and it proves it in front of an
audience of exactly the right people, who already run several coding agents at
once and already feel the pain Miranda solves. herdr's own remote answer is
SSH, which needs a reachable host, a key on the device in hand, and no phone
story. `mir up --engine herdr` is one line on top of a setup they already
have. Technically, the seam pays for itself before herdr ships: `Engine`
replaces `isDefaultTmuxLaunch`'s byte-exact argv match and
`sessionFromLaunch`'s one-predicate feature gate with something a person can
reason about. And `agent_status` is a genuinely new capability — an overview
that says which machine is *blocked* is a better overview than one that says
which machine is online.

**What Miranda must not promise.** Not "full Miranda on herdr" — on herdr
0.8.2 two people cannot watch one session with independent views, and that is
Miranda's own headline scenario, so `--engine herdr` is single-viewer until
herdr says otherwise. Not "we know your agent is blocked" — that is herdr's
heuristic over a screenshot, refreshed from herdr.dev, and Miranda should say
whose opinion it is displaying. Not "read-only guests see what you see" — a
narrow viewport is a crop, so a phone guest sees the left edge of a wide pane.
And not a smaller trusted base than we have: a default herdr server polls
herdr.dev for versions and detection manifests, which is a network dependency
inside a product whose pitch is a relay that cannot read anything. That is a
conscious trade to make in the open, or to disable in Miranda's config
template.

**The honest risk.** herdr may build its own remote layer. It already ships
`--remote` over SSH, a client/server split, a protocol with a version integer,
and a mobile-shaped Agents list — every piece except the relay and the identity
model. If they add a hosted relay, the distribution play becomes a competitive
one overnight, and Miranda's answer has to be the part herdr has shown no
interest in: passkey identity, a relay that cannot read the session, signed
grants, and continuity across *machines you own* rather than one host you SSH
to. Two things follow. Move while the door is open — E3 through E6 are small,
and the argument is strongest before herdr has an opinion about remote.
And keep the seam symmetrical: it should be as easy for herdr's users to reach
Miranda as it would be for Miranda to be replaced, because a seam that only
works one way is a dependency, not a strategy.

---

## Open questions for Fredrik

1. **Refusing the second attach on herdr.** §3.4 makes a herdr machine
   single-viewer, which contradicts the product's own headline. The
   alternative — let both attach and let them fight — is what D4 deleted.
   Refuse, or allow with a loud warning?
2. **Two window strips.** On herdr the engine draws its own tab bar inside the
   pane, and Miranda's strip sits above it. Live with the duplication, hide
   Miranda's strip when the engine draws its own, or hide the engine's?
3. **Writable guests on herdr.** `terminal session control` is one pane,
   exclusive, and anyone can `--takeover` anyone. Ship `mir share --write` as
   pane-scoped on herdr, or refuse write sharing on herdr entirely in v1?
4. **herdr's phone-home.** Version checks and detection-manifest updates
   against herdr.dev are on by default. Ship Miranda's herdr config with both
   off, and accept staler agent detection — or leave herdr's defaults alone and
   document it?
5. **Scope.** E1+E2 (the seam, tmux only) is worth landing regardless — it
   deletes the argv-equality gate. Is E3 onward a now thing, or does it wait
   behind the v0.8 beta?

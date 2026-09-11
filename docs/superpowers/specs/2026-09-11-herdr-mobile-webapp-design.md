# Herdr-aware mobile webapp — the inbox herdr does not have

> Investigation + design; no build. Grounded in herdr **0.9.0** (protocol 22,
> endpoint generation 1) running on linux/x86_64, and in the existing Miranda
> PWA. Where a thing was not measured, it says so. The 2026-08-31 engine-seam
> spec remains the source of truth for the `Engine` interface; this spec
> decides what the **phone** should be.

## One sentence

Miranda already ships the mobile client herdr refused to build; the webapp
should become an **agent inbox across paired machines**, not a phone-sized
dump of herdr's TUI.

## Why this now

herdr 0.9.0 (2026-09-07) is the first version that can put several machines in
one window. That is the orchestration people want. It is still SSH, still a
desktop TUI, still nothing you open on a phone with a passkey. Herdr Cloud
is on a waitlist as the "middleman layer" that would connect those machines
without SSH. That middleman is Miranda's whole product. The door is open
until they ship it.

The existing SPA already does the reach half: passkey, pairing, encrypted
registry, a warm pool of three machines, a touch keybar, PWA install. What
it does not do is speak herdr — so a herdr box today is either tmux, or
`--shell herdr` painting a mouse-first desktop TUI into xterm.js.

---

## Part 1 — what is actually out there

### 1.1 Miranda's webapp, today

A vanilla-JS PWA (`web/`). Passkey identity, in-browser pairing, a machine
grid, attach over WebRTC + Noise, xterm.js, tmux window strip, up to three
machines warm, park-on-hide, mobile keybar (Esc / Ctrl / Tab / arrows). It is
already the "from my iPhone" path from plan 6b. It knows windows. It does
not know agents.

### 1.2 herdr 0.9.0 — orchestration, still no phone

Two things shipped that the August spec could not see:

1. **`herdr machine add workbox`.** One TUI client SSHes to N herdr
   servers. The sidebar is a combined agent list. A stalled machine does not
   take the others down. Profiles store an opaque id, a label, an SSH
   target, an optional named session, and enabled/disabled — not keys. This
   is client-side. The socket API on a server has **102 request methods and
   zero `machine.*` methods**; `herdr machine list` against a headless
   server prints `No saved SSH machines` / `[]`. Cross-machine `agent wait`
   is explicitly not in 0.9: the CLI still talks to one server.
2. **Independent TUI views (#3526).** "Multiple clients can now view different
   workspaces and tabs independently. Different tabs fit their viewing
   clients; when clients share a tab, the last one to interact with it
   controls its size." The outer UI now runs in the client; servers own
   panes and feed terminal views. This is the D4-shaped bug from 0.8.2,
   addressed for **herdr TUI clients**.

What 0.9 still does not ship: a browser, a phone app, a relay, passkeys,
sharing. Remote remains SSH (or Tailscale in front of SSH). The installer
tells you Android/Termux is unsupported.

herdr's own TUI has a mobile layout (`mobile_width_threshold = 64` columns)
and a touch-oriented switcher. That is a narrow *terminal* layout, not a
webapp. It still needs a route to the host.

### 1.3 The community already tried to fill the hole

| Project | What it is | What it needs | Why it is not us |
|---|---|---|---|
| [Moshi](https://getmoshi.app/guides/herdr) | Native iOS terminal with a herdr-aware picker and a Needs you / Working / Done inbox | SSH to the host, `herdr` on PATH in a non-interactive shell | SSH keys on the phone; no passkey; one vendor's app |
| [kcosr/herdr-web](https://github.com/kcosr/herdr-web) | Browser TUI + `herdr-web-bridge` | Vendored private herdr APIs; LAN/bridge URL; one attach owner per terminal, fanned out by the bridge | Private APIs; no identity model; the author asks herdr to expose a supported web surface |
| [benkraus/herdr-plugin-mobile-relay](https://github.com/benkraus/herdr-plugin-mobile-relay) | Loopback HTTP/WS relay + optional Tailscale Serve | Bun, Tailscale, a single-user host | Tailscale is the reach layer; the relay sees more than Miranda's does |

They prove the gap is real. None of them is a blind relay plus a passkey.

### 1.4 The layer split, drawn once

```text
                    herdr 0.9                         Miranda
  machine-local     workspaces, tabs, panes,           (not our job)
                    agent state, observe/control

  multi-machine     TUI client over SSH                passkey + paired
                    (`machine add`); Cloud waitlist    machines + registry

  phone / browser   TUI-at-64-cols; Moshi/SSH;        the PWA we already
                    community bridges                   ship
```

herdr's new orchestration is the *client* aggregating SSH servers. Miranda
already aggregates machines at the identity layer. The webapp should show
**one inbox of agents**, each row carrying the Miranda machine it lives on,
and attach through Miranda — never through `herdr machine add`.

---

## Part 2 — measured on 0.9.0

Binary `herdr-linux-x86_64` from `herdr.dev/latest.json`, SHA-256
`4fa1a01158dd8043da92d31b270780b0dcc10603038d9b61cac4d81ab63fb71f`. Private
`HOME=/tmp/h9`, `HERDR_SOCKET_PATH=/tmp/h9/herdr.sock`,
`version_check = false`, `manifest_check = false`. Headless `herdr server`.

### 2.1 Headless server starts empty

`herdr api snapshot` on a fresh server:

```json
{"agents":[],"layouts":[],"panes":[],"tabs":[],"workspaces":[],"protocol":22,"version":"0.9.0"}
```

`herdr workspace create --label demo` minted `w1` / `w1:t1` / `w1:p1` and
a `terminal_id`. A Miranda engine that talks to a headless server cannot
assume a default session the way `tmux new -A -s main` does. `Launch` must
create a workspace if the snapshot is empty, or attach as the TUI binary
(which does that itself).

### 2.2 Observe is still confined

`herdr terminal session observe w1:p1` while `TAB_TWO_MARKER_SECRET` was
printed in `w1:p2`: **0 hits** in the observer's decoded frames.
`RO_PROBE_PANE1_AGAIN` printed in `w1:p1`: **present**. Same shape as 0.8.2.

### 2.3 Control is still exclusive, with takeover

With stdin held open:

- Second `terminal session control` on the same pane, no `--takeover`:
  `terminal attach failed: terminal term_… already has an attached client;
  retry with --takeover`.
- With `--takeover`, the incumbent received
  `terminal attach taken over` and the new controller stayed up.

A run that did **not** keep stdin open both exited immediately with
`reason: detached`. That is a client lifecycle quirk, not a new sharing
model. Do not cite it as "control is no longer exclusive."

### 2.4 Agent state still rolls up

```
herdr pane report-agent w1:p1 --source probe --agent claude --state blocked \
  --message "needs approval"
```

Then `agent list`, the pane, its tab, and its workspace all reported
`blocked`. Same as 0.8.2. Still a heuristic when detection is a screen
manifest; this path was the probe/report API, not a live Claude Code pane.

### 2.5 `tab.focus` is still server-global

`herdr tab focus w1:t2` flipped `snapshot.focused_tab_id` to `w1:t2`.
That is the server's focused tab, not a per-client cursor. A Miranda
window strip that maps `select-window` onto `tab.focus` will yank every
API consumer and, from 0.8.2, yanked TUI clients too.

`client_shell.surface.set` exists in protocol 22. Its params are
`{ "active": bool }` — whether *this* client shell receives pane presentation.
It is not a per-tab view selector.

### 2.6 Independent TUI views — documented, not re-measured here

0.9's #3526 and the Connecting machines doc claim per-client workspace/tab
views for TUI clients. Two PTY `herdr` attaches in this sandbox landed in
the first-run integrations overlay (`claude not found`, `↵ continue`), so
a clean A-follows-B replay of the 0.8.2 experiment was **not** completed.
Treat per-client TUI views as herdr's published contract, and treat
`tab.focus` via the socket as still global until a later slice re-measures
both on Linux with onboarding dismissed.

### 2.7 Geometry / crop — not re-measured

0.8.2: a controller's `--cols/--rows` resizes the pty only when no full-UI
client owns geometry; otherwise a small viewport is a **crop**. 0.9's
shared-tab rule (last interactor wins size) is documented for TUI clients
sharing a tab. Whether a `terminal session control` stream vs a desktop TUI
still crops a phone was **not** re-run. Carry the 0.8.2 crop warning into
v1 copy until M3's acceptance re-measures it.

### 2.8 Phone-home and sun_path — unchanged

Defaults: `version_check = true`, `manifest_check = true`. Socket under
`$HOME/.config/herdr/herdr.sock`. `HERDR_SOCKET_PATH` still the escape
hatch; Miranda should keep setting it under its own state dir.

---

## Part 3 — the product

### 3.1 Do not paint herdr's TUI on a phone

`--shell herdr` already "works": the browser sees herdr's sidebar, tab bar,
and mouse UI inside xterm.js. On a phone that is two chromes (theirs plus
Miranda's strip) and a 64-column mobile TUI that still wants a prefix key.
Moshi exists because that is painful. Community web bridges exist because
that is painful.

v1 of this webapp **does not attach as the herdr TUI**. It uses the
third-party-bridge streams herdr already documents:

- snapshot + `events.subscribe` → the inbox
- `terminal session observe <pane>` → read-only view of one pane
- `terminal session control <pane>` → writable view of one pane, exclusive

Miranda's existing machine pool stays the multi-machine layer. herdr's
`machine add` is out of scope on the agent; we do not SSH from the phone.

### 3.2 Three screens

1. **Machines** — today's grid, plus a rollup when the engine reports agent
   state: `blocked 2 · working 1` on the card. tmux cards stay as they are
   (no `st`). A machine whose HELLO says `engine: herdr` is allowed to look
   different; a tmux machine must not grow fake agent dots.
2. **Inbox** — one list, every paired herdr machine, sorted by attention
   (`blocked`, `done`, `working`, `idle`, `unknown`). Each row: machine name,
   workspace/tab label, agent name, state. Tap → that pane on that machine.
   This is the thing herdr 0.9's sidebar does over SSH, and Moshi's inbox
   does over SSH. We do it over the already-paired Noise channel.
3. **Pane** — one xterm, sized to the phone, with today's keybar. Chrome is
   Miranda's: back, machine chip, state pill, optional Observe/Control
   toggle. No herdr tab bar inside the pane. Window-strip verbs that would
   call `tab.focus` / `workspace.focus` are hidden on this surface — they
   are server-global and would yank a desktop herdr client.

A tmux machine skips the inbox and opens today's terminal + window strip.
Honest degradation: no agent list, not a greyed-out fake one.

### 3.3 Attach model on herdr

| Role | Stream | Who resizes | Who can type |
|---|---|---|---|
| Owner on phone, no desktop TUI | `control` | the phone (pty reflows) | this client |
| Owner on phone, desktop TUI already on that pane | `observe` by default; `control --takeover` only behind a named confirm | 0.8.2: crop if you control anyway | takeover evicts the desktop |
| Guest read-only | `observe` | never | nobody |
| Second phone on the same pane | `observe` (unlimited) or refuse a second `control` with herdr's own error | — | — |

This *is* per-attach isolation for the case Miranda actually needs on a
phone: two people (or phone + desktop) looking at the **same** running
agent without sharing a TUI focus. It is not tmux grouped sessions. It is
better than dumping the TUI and hoping #3526 holds.

`Launch` for the herdr engine therefore has two modes, and the webapp uses
the second:

- **TUI launch** (CLI `mir attach`, optional): argv `herdr`, user sees
  herdr's own interface. Fine on a laptop terminal. Subject to 0.9's
  TUI-view rules.
- **Pane launch** (webapp v1): no PTY herdr client. The agent opens
  observe/control against a `pane_id` and copies ANSI frames onto
  `FrameData`. Resize of a control stream maps to `terminal.resize`, not to
  the PTY of a herdr TUI.

FrameWindows v2 still carries the tree so the CLI overview and any desktop
strip keep working. The inbox is a new consumer of the same snapshot, plus
the optional `st` field the engine spec already specified.

### 3.4 HELLO carries enough to pick a chrome

Today HELLO is `{"name": "…"}`. Add optional fields, ignored by old clients:

```json
{"name":"workbox","engine":"herdr","caps":{"agent_state":true,"confined_mirror":true,"per_attach_view":false}}
```

The SPA branches on `engine` / `caps.agent_state`, never on a user-agent
string. A missing `engine` means tmux-shaped, as today.

No new frame type for v1. Agent rows ride on FrameWindows (`st`) plus the
existing snapshot. If a later slice needs `agent.explain` / prompt text,
that is a new frame, as the engine spec already said.

### 3.5 What we must not promise

- **"The same independent views as tmux."** Pane observe is independent;
  `tab.focus` is not; TUI independence is herdr's contract, not ours to
  restated as measured until Linux says so.
- **"We know Claude is blocked."** Still herdr's opinion. Label it.
- **"Your phone reflows the desktop pane."** Crop until re-measured; v1
  prefers observe when a TUI owns geometry.
- **Cross-machine agent orchestration.** herdr 0.9 cannot `agent wait`
  across SSH machines either. Miranda's inbox is a *view* across machines,
  not a scheduler.
- **A smaller trusted base if phone-home stays on.** Same open decision as
  the engine spec.

---

## Part 4 — how this changes the engine spec

Keep E1 (extract `Engine` around tmux) and E2 (`--engine`, doctor, caps).
They pay for themselves with no herdr.

Revise the herdr slices for a web-first v1:

| Engine spec | Mobile webapp |
|---|---|
| E3 Launch = `herdr` TUI in the PTY | Keep for CLI. Web v1 uses pane observe/control instead |
| E4 Control = six window verbs via `tab.*` | Hide `tab.focus` / `workspace.focus` on the phone surface; they are server-global |
| E5 Refuse a second interactive attach | For **TUI launch**, follow 0.9's published multi-client contract (re-measure). For **pane launch**, allow many observers; refuse a second controller without takeover |
| E6 Mirror via observe | This becomes the primary phone view, not just G1 guests |
| E7 `st` on FrameWindows | The inbox's data path; do this earlier than the engine spec's order |
| E8 Bootstrap / install | Still needed for `mir up --engine herdr` |

The rule does not bend: no silent degradation. A second `control` on a pane
is herdr's error, surfaced with the engine named. A tmux machine never
grows an inbox.

Open questions 1 and 2 from the engine spec look different from the phone:

1. **Second attach.** For the webapp, do not refuse the second *machine*
   attach (R2 already warms three). Refuse a second *controller* of one
   pane. A second TUI `mir attach` on a laptop is herdr's problem; our
   recommendation is still "allow with a loud warning" until Linux
   re-measures #3526.
2. **Two window strips.** On the phone, draw neither herdr's nor a tmux
   strip. Draw the inbox and one pane. On a desktop browser, a herdr TUI
   launch may still double-chrome; that path is not v1.

3–5 (writable guests, phone-home, sequencing) stand. Recommendation for
sequencing **changes**: do not hold herdr behind the rest of the beta.
Herdr Cloud is the reason to move. E1 → E2 → (herdr Probe/Snapshot/Watch +
Mirror) → inbox UI is the shortest honest path to a webapp that is *about*
herdr. Full TUI launch and window-verb parity can follow.

---

## Part 5 — slices

| # | Slice | Acceptance |
|---|---|---|
| M0 | This spec. No code. | — |
| M1 | E1 + E2 as already specified (tmux-only seam). | `go test ./...` and `npm test` green; `mir doctor` prints tmux caps |
| M2 | herdr engine: Probe, Snapshot from `session.snapshot`, Watch from `events.subscribe`. Empty snapshot → `workspace create`. Socket via `HERDR_SOCKET_PATH`. Read-only. | CLI overview and web strip show herdr tabs; a `tab create` appears in the strip without polling |
| M3 | herdr `Mirror` (`observe`) and pane `control`. Agent copies ANSI frames onto `FrameData`. HELLO grows `engine` + `caps`. | Phone-sized xterm shows one pane; observe confinement test from the engine spec (no inject, no other-pane leak). Second control without takeover fails with a named error |
| M4 | `st` on FrameWindows v2 (engine E7). Inbox UI in the SPA: cross-machine list, tap to M3. tmux machines have no inbox. | Old client + new agent: today's UI. New client + tmux: today's UI. New client + herdr: inbox with state. One vector added, none changed |
| M5 | Desktop-TUI coexistence: if a controller cannot resize (crop), the SPA stays on observe and says so. Takeover is a confirm sheet that names the engine. | Copy is tested; no silent crop of a 113-column desktop pane onto a 40-column phone |
| M6 | Bootstrap: `mir up --engine herdr` (engine E8). | Clean machine: prompt → install → up → inbox on the PWA |

Order M0 → M1 → M2 → M3 → M4. M5 can land with M3 if the crop re-measure is
cheap. M6 can parallel M4.

Out of scope for this spec: herdr plugins, graphics, worktrees, `agent prompt`
from the phone, push notifications, rewriting the PWA in a framework,
contributing a web protocol upstream.

---

## Open questions for Fredrik

1. **Inbox as the herdr home screen, or keep the machine grid first?**
   Recommendation: keep the grid (it is also the tmux home, and the pairing
   empty state). Inbox is a second entry from the grid and from a top-level
   "needs you" chip when any machine is `blocked`.
2. **Writable phone on a pane a desktop TUI is watching.** Recommendation:
   observe by default, takeover behind a confirm. Never silently `--takeover`.
3. **Phone-home.** Unchanged: Miranda's herdr config template sets
   `version_check` and `manifest_check` off.
4. **Ship M1 now** (pure refactor) **even if M2 waits?** Yes. The argv-equality
   gate should die regardless of herdr.

---

## Competitive note

herdr 0.9 is a TUI mesh over SSH. Herdr Cloud is a waitlist for "connect any
machine, anywhere, through one Herdr account" with E2E encryption — the
sentence that is already Miranda's README. Moshi is a good phone terminal
for people who already SSH. The distribution play is unchanged and more
urgent: `mir up --engine herdr` is one line on a setup they have, and the
PWA is the client their 0.9 sidebar cannot be.

Move while the door is open. Keep the seam symmetrical: herdr users should
reach Miranda as easily as herdr could later replace the relay. A seam that
only works one way is a dependency, not a strategy.

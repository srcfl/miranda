# Status — where Miranda stands

Written 2026-08-31, updated 2026-09-11. Read this first if you are picking the
project up cold, then `CLAUDE.md`, `docs/product.md`, and the spec for whatever
you are about to touch. This file records state and decisions; the specs remain
the source of truth for design.

**Next goal: run every herdr from the phone.** `mir up --engine herdr` on
each box; the existing PWA attaches to that machine's herdr TUI. The agent
inbox is later. Jump to [What comes next](#what-comes-next).

## Where things stand

Latest release **`v0.8.0-beta.5`** (prerelease, 15 cosign-signed assets). The
relay and the browser app were redeployed from `main` on 2026-08-30 and verified
live: every importmap entry resolves, the beta's modules are served,
`/registry?wallet=…` and `/turn-credentials` answer.

Miranda is positioned as **the reach layer for persistent terminals**: a
multiplexer keeps the session alive on the machine, Miranda gets you to that
machine. The engine is tmux today; herdr is the second engine, and the
product-facing goal is a herdr-aware mobile webapp on the PWA we already
ship. The name's story now ships too — "a relay that cannot testify."

Shipped over 2026-08-29/30, all merged and released:

| Area | State |
|---|---|
| First run | `mir up` prints the pairing QR itself and offers to install tmux |
| Entry | bare `mir` opens a live machine overview; `mir a` / `mir ls` / `mir id`; bare `mir attach` resumes the last machine |
| Transport | **one** connection, two ICE modes (direct, TURN fallback). The mDNS+QUIC path is deleted |
| Speed | attach 12 ms LAN host pair / 17 ms open / ~1.0 s TURN; resume 2352 ms direct, 2768 ms relayed — `netsim/results/results.md`, rerun with `make netsim` |
| Pre-attach | the three relay round trips run in parallel behind a stale-while-revalidate cache (~0 ms warm) |
| tmux | window strip pushed from tmux hooks (~62 ms); each attach gets its own grouped session, so viewers no longer fight over the current window |
| Web | up to three machines warm at once, instant switching, parked when the tab hides |
| Sharing | `mir share` / `mir join`: owner-signed, guest-key-bound, expiring grants; read-only by default, enforced at the agent |
| Care | rename and retire flows, error taxonomy, `mir doctor --share` (redaction is test-enforced) |
| Beta program | `BETA.md`, issue templates, `docs/beta-checklist.md` |

## What comes next

**Ship phone attach to the herdr you already run.** Spec:
[`docs/superpowers/specs/2026-09-11-herdr-mobile-webapp-design.md`](superpowers/specs/2026-09-11-herdr-mobile-webapp-design.md)
(v1 is TUI-in-PWA; inbox is the following product). The engine seam
([#107](https://github.com/srcfl/miranda/issues/107),
[`2026-08-31-engine-seam-herdr-design.md`](superpowers/specs/2026-08-31-engine-seam-herdr-design.md))
still pays for itself, but it does not gate this slice: `mir up --engine herdr`
launches `herdr` in the PTY, HELLO carries `engine`, the PWA hides the tmux
strip. Do not isolate `HERDR_SOCKET_PATH` — that would be a second, empty
session.

Why: herdr 0.9.0 (2026-09-07) can orchestrate several machines in one TUI —
via SSH (`herdr machine add`). It still has no browser, no passkey, no
phone. Community fills that with Moshi (SSH), `herdr-web` (private APIs),
and a Tailscale relay plugin. Miranda's PWA already is that client at the
reach layer. Herdr Cloud is waitlisted as an E2E "middleman" — the sentence
that is already the README. Move while that door is open.

Everything below about streams was **re-measured against herdr 0.9.0**
on linux/x86_64 (protocol 22). TUI-view independence (#3526) is herdr's
published contract; a clean A-follows-B replay was not completed in the
sandbox (first-run overlay). Details live in the 2026-09-11 spec §2.

- **Better than tmux, still:** `terminal session observe` is confined
  (a marker printed in another pane did not appear). `events.subscribe`
  still beats R3's hooks. Agent state still rolls up pane → tab →
  workspace. `report-agent` to `blocked` was visible on all three.
- **Worse than tmux, still, for the socket:** `tab.focus` is
  server-global. Control of one pane is exclusive; `--takeover` evicts
  the incumbent. A headless server starts with an empty snapshot — there
  is no `tmux new -A -s main` analogue until `workspace create`.
- **Changed since 0.8.2:** herdr TUI clients are documented as able to view
  different tabs independently, with last-interactor size on a shared tab.
  Multi-machine lives in the *client* over SSH; the socket has no
  `machine.*` methods. Do not implement `herdr machine add` inside Miranda.
- **Watch out, unchanged:** phone-home on by default; `sun_path` overflow
  on a long `HOME`; a small viewport is a crop when a full TUI owns
  geometry (0.8.2 measurement, not re-run on 0.9).

### Decisions still open (Fredrik's call)

The engine spec's five, plus two from the webapp spec. Recommendations
below; none is ruled on yet.

1. **A second interactive attach on herdr** — for pane-streams: many
   observers, refuse a second controller without takeover. For TUI launch
   (`mir attach` on a laptop): allow with a loud warning until #3526 is
   re-measured on Linux.
2. **Two window strips** — on the phone, draw neither. Inbox + one pane.
   Desktop TUI launch may still double-chrome; that path is not web v1.
3. **Writable guests on herdr** — still **refuse `--write` in v1**.
   Observe-based sharing is the better primitive.
4. **herdr's phone-home** — ship Miranda's herdr config with it off.
5. **Sequencing** — land E1+E2 (tmux seam) regardless. Do **not** hold the
   herdr engine behind the rest of the beta; the webapp spec's M2–M4 are
   the product. Herdr Cloud is why.
6. **Inbox vs machine grid as home.** Recommended: keep the grid; a
   "needs you" chip opens the inbox when any herdr machine is `blocked`.
7. **Phone typing at a pane a desktop TUI is watching.** Recommended:
   observe by default; `--takeover` only behind a confirm that names the
   engine.

The rule the spec sets, and the one not to bend: **no feature may silently
degrade.** If an engine cannot do per-attach isolation or a confined read-only
view, Miranda refuses or says so plainly.

## Waiting on Fredrik

- **Upload `assets/social-preview.png`** — GitHub → Settings → General → Social
  preview. No CLI or API path exists.
- **P1: the passkey/browser matrix** on real devices. `docs/beta-checklist.md`
  is written to be handed to a tester.
- **P3: commission the external audit.** Scope is ready in
  `docs/audit-scope.md`; `BETA.md` states plainly that no audit has been
  commissioned.
- The herdr decisions in [What comes next](#what-comes-next) (now seven).

## Open backlog

- [#113](https://github.com/srcfl/miranda/issues/113) the 📱 in share/pair output
  is a dark blob on dark terminals — small copy call.
- [#114](https://github.com/srcfl/miranda/issues/114) `mir-signal` reads as
  Signal the messenger. Decide together with the module-path debt
  (`github.com/srcful/terminal-relay/go`), never piecemeal.
- [#19–#23](https://github.com/srcfl/miranda/issues) from an earlier security
  review: SAS width, registration-proof squat window, non-root agent, an
  RP-ID overstatement in SECURITY.md, and a residual TOCTOU in `redeploy.sh`.
- [#55](https://github.com/srcfl/miranda/issues/55) is the beta tracking issue
  and carries the full slice history.
- Deploy hardening noticed on 2026-08-30, not filed: the health gate rolls back
  the binary and unit but **not** the webroot, and does not gate on the SPA's
  status code; and `redeploy.sh` builds without version ldflags, so the live
  binary reports `mir-signal dev (none, unknown)`.

## How this repo has been worked

- **Work from issues, one logical change per PR, squash merge.** Branch names
  `{issue}-short-description`.
- **The gates are not optional.** `cd go && go test ./...`, `gofmt -l .` empty,
  `go vet` clean, `cd web && npm test`. Go and JS crypto stay byte-identical —
  `testdata/` vectors are the gate.
- **Claims must be checkable.** Quote `netsim/results/results.md` for numbers,
  never memory. Every security sentence must match merged code; three claims
  were cut as overstated during the positioning pass for exactly this reason.
- **Model routing** (Fredrik, 2026-08-30): implementation goes to subagents
  running Opus; the main thread does design, specs, review and merge decisions.
- **Live infrastructure is Fredrik's keystroke.** Claude prepares a relay
  deploy — including the SPA digest — and he runs it. Merging and cutting
  releases are delegated.

## Traps that have already bitten us

Each of these cost real time. They are recorded so they cost none again.

1. **Go's `flag` stops parsing at the first positional.** This shipped broken
   three times before a shared `parseArgs` helper and a table test fixed the
   whole class. Any new command taking positionals *and* flags must go through
   it.
2. **A new relay route must be added to `withStatic`'s `signalPaths` in
   `cmd/mir-signal/main.go` AND to `sw.js`'s `SIGNALING`** — tests that exercise
   `Server.Handler()` directly will not catch a 404 in production.
3. **Every bare `@` import under `web/src` must be in the index.html importmap**
   (`web/test/importmap.test.js` gates it). Missing one black-screened the app
   in v0.5.1 while all tests passed.
4. **New web files need a `sw.js` precache entry and a cache-version bump.**
5. **Do not pipe `gh pr checks --watch` through `tail`** — it eats the exit code,
   and a PR once merged on a red check because of it.
6. **Overclaiming is the easiest mistake to make here.** The relay *does*
   persist owner-signed revocation tombstones; the CLI *does* keep an owner root
   in the OS keychain. Bound every guarantee in the same paragraph that makes
   it.

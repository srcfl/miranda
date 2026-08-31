# Status — where Miranda stands

Written 2026-08-31. Read this first if you are picking the project up cold, then
`CLAUDE.md`, `docs/product.md`, and the spec for whatever you are about to touch.
This file records state and decisions; the specs remain the source of truth for
design.

**Next goal: carry herdr as a second engine.** Jump to
[What comes next](#what-comes-next).

## Where things stand

Latest release **`v0.8.0-beta.5`** (prerelease, 15 cosign-signed assets). The
relay and the browser app were redeployed from `main` on 2026-08-30 and verified
live: every importmap entry resolves, the beta's modules are served,
`/registry?wallet=…` and `/turn-credentials` answer.

Miranda is positioned as **the reach layer for persistent terminals**: a
multiplexer keeps the session alive on the machine, Miranda gets you to that
machine. The engine is tmux today, and carrying a second one is the next goal.
The name's story now ships too — "a relay that cannot testify."

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

**Carry herdr as a second engine.** Spec:
[`docs/superpowers/specs/2026-08-31-engine-seam-herdr-design.md`](superpowers/specs/2026-08-31-engine-seam-herdr-design.md),
issue [#107](https://github.com/srcfl/miranda/issues/107). Its slice table is
authoritative; this is the orientation.

Why: [herdr](https://github.com/herdrdev/herdr) is an agent-aware tmux
replacement whose remote answer is SSH into your own box. It is not a
competitor — it sits one layer below Miranda, and its audience is Miranda's
ideal user, already assembled. Every engine Miranda can carry widens the market
instead of splitting it.

Everything below was **measured against herdr 0.8.2**, not assumed:

- **Better than tmux:** `terminal session observe` is a genuinely confined
  read-only stream — injected input, resize and scroll all failed to reach the
  pane, and a focus change elsewhere never leaked into the stream. That is a
  cleaner primitive than G1's `capture-pane` + `pipe-pane`. Its event socket
  also beats R3's 22 hooks, and it exposes agent state (working / blocked /
  idle) that tmux never had — a new capability, not just parity.
- **Worse than tmux:** there is **no per-attach view isolation** — two clients
  share focus, measured both at tab and workspace level. That is exactly the bug
  D4's grouped sessions were built to kill. A writable guest is one pane,
  exclusive, and any client can `--takeover` and evict the incumbent.
- **Watch out:** herdr phones home by default (version checks and the agent
  detection manifests), and its socket lives under `$HOME`, so a long `HOME`
  overflows `sun_path`.

### Decisions still open (Fredrik's call)

Claude's recommendations, argued in the spec and in the session that produced
it; none is ruled on yet.

1. **A second interactive attach on herdr** — allow it with a loud warning
   (recommended: refusing would make a herdr box *worse* with Miranda than
   without, which kills the distribution play), or refuse it.
2. **Two window strips** — herdr draws its own tab bar inside the pane. Hide
   ours, hide nothing, or keep ours only where it adds tap targets.
3. **Writable guests on herdr** — recommended: **refuse `--write` in v1**, since
   any client can evict the owner. Read-only sharing there is better than on
   tmux, so the sharing story survives.
4. **herdr's phone-home** — recommended: ship Miranda's herdr config with it
   off, and document that agent detection goes staler.
5. **Sequencing** — recommended: land the seam for tmux only (a pure refactor
   that pays off regardless), and hold the herdr engine itself until the beta
   gaps below are closed.

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
- The five herdr decisions above.

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

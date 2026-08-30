# G1 — guest session sharing: design + security review

> Implements decision D5 of
> `2026-08-30-invisible-tmux-one-path-design.md`. Spec only — the build is
> gated on this review. Composes existing parts: Ed25519 owner identity
> (B1), the NNpsk0 pairing room (plan 5), Noise-KK attach, D4 grouped
> sessions, and the N1-style CONTROL delivery. No new crypto primitives, no
> relay changes, no relay state.

## One sentence

An owner mints a signed, expiring, guest-key-bound grant for one machine's
terminal; the guest joins with a one-time code or link; the agent enforces
mode and expiry at the PTY; the relay learns nothing new.

## What tmux can and cannot confine (measured, tmux 3.7b)

Every claim below was verified on a private tmux server on this machine:

1. A read-only client (`attach -r`) cannot type into a pane and cannot use the
   command prompt (`C-b : kill-window` did nothing), but **it CAN
   switch-client**: a `-r` client on session `main` pressed `C-b )` and landed
   on an unrelated session `private`. tmux documents `-r` as leaving exactly
   detach and switch-client keys live, and it does. **An interactive read-only
   tmux client is therefore server-wide viewing, not confinement.**
2. `attach -f read-only,ignore-size` keeps the flags and leaves window size
   untouched (window stayed 200×50 under a 60×20 client). A plain `-r` attach
   resized the window to 60×19 despite reporting an `ignore-size` flag — so
   sizing must always be pinned with the explicit flag AND a regression test.
3. `link-window` builds a session containing exactly one shared window;
   killing that session leaves the window alive in `main`. Sessions confine
   what they *contain* — but per (1), not what an interactive client can *see*.
4. `capture-pane -e` (initial paint, escapes included) + `pipe-pane -O` (live
   output stream) faithfully mirror one pane with **no tmux client at all**.

Consequence — the central design call of this spec:

- **Read-only (the default) is a pane mirror, not a tmux client.** The agent
  streams `capture-pane -e` + `pipe-pane -O`; guest input is discarded at the
  agent's frame loop, never forwarded. The guest reaches exactly one pane's
  output. Hard confinement, because no tmux client exists to switch anywhere.
- **Write mode is a grouped session and is total.** A shell IS arbitrary code
  execution as the agent's user, and that shell can drive the whole tmux
  server through its socket. No per-session confinement survives contact with
  a writable shell, so the spec does not pretend otherwise: `--write` means
  "this person can do everything you can do on that machine", and the UX says
  exactly that before minting.

## 1. The grant

Canonical bytes follow B1.2's pattern (`identity/binding.go` `Canonical()`):
fixed field order, hand-built concatenation, every field validated to need no
JSON escaping, byte-identical between Go and JS, gated by a new
`testdata/grant.json` vector.

    {"v":1,"owner":"<base58>","machine":"<machine_id>","guest":"<base58>",
     "scope":"<tmux session name>","mode":"ro","nb":<unix>,"na":<unix>,
     "gid":"<16 lowercase hex>"}

- `owner` — the minting owner's base58 wallet (must be pinned on the agent).
- `machine` — `machineIDRe` charset, as in revocations.
- `guest` — the guest's base58 wallet, **bound at claim time** (§2): the grant
  is signed only after the guest's key has arrived through the encrypted
  room, so a grant is never a bearer token; stolen, it is useless without the
  guest's private key.
- `scope` — tmux session name (default `main`), charset `[0-9A-Za-z._-]{1,64}`.
  In ro mode the mirrored pane is the scope session's active pane, resolved at
  attach. In rw mode the guest's grouped session joins the scope's group.
- `mode` — `ro` | `rw`. Default `ro`; `rw` only via `--write`.
- `nb`/`na` — unix seconds. `nb = mint time − 300` (five minutes of skew
  tolerance, matching `mir doctor`'s clock-skew threshold); `na = mint time +
  TTL`. TTL defaults to 1 h, hard cap 24 h: long enough for a working
  session, short enough that the backstop in §4 is real.
- `gid` — 16 hex chars from crypto/rand; names the grant for revocation.

Signature: `sig = Ed25519(owner_priv, "miranda/grant/v1" || canonical)`,
base58, appended as `,"sig":"…"}` exactly like `SignedBinding.JSON`. The agent
verifies against the pinned owner key it already holds — **grants from a
wallet that is not currently pinned are dead**, which also answers identity
rotation: rotating the owner re-pairs the agent and every old grant fails
verification against nothing.

## 2. Bootstrap — the invite rides the pairing room

`mir share` reuses the pairing machinery (`internal/pairing`) with the roles
it already defines; nothing new for the relay, whose pair-room bridge stays
byte-blind and rate-capped as in #54.

1. Owner runs `mir share <machine> [--ttl 1h] [--write] [--session <s>]`.
   The client mints a fresh 16-byte one-time token, prints the code, a QR,
   and the web link (`<web>/#join-<code>`), and waits as the room
   **responder** — the same posture as `mir pair` serving an owner.
2. Guest runs `mir join <code>` (or opens the link; the SPA join view drives
   the same flow). The guest client creates its own Miranda identity if none
   exists (the normal first-run path), then joins the room as **initiator**
   over NNpsk0 with the token-derived PSK — one claim consumes the room, as
   with pairing.
3. Inside the encrypted channel the guest sends its wallet address and a
   `miranda/binding/v1` signed transport binding for its X25519 key (the same
   record every owner device already presents).
4. The owner's terminal shows the **safety number** plus the guest wallet's
   short id and waits for an explicit `y/N`. The owner is the trust decision
   here — the guest just joined and risks nothing; the asymmetry is
   deliberate. Non-interactive minting is refused (no `--yes`; sharing a
   shell is not a scripting primitive).
5. On confirm, the owner signs the grant binding the guest's wallet, sends
   the guest {grant, machine_id, signal URL, host_pub, the owner's binding}
   through the room, and — over a normal authenticated attach to the machine —
   delivers `{a:"add-grant", grant:…}` as an agent-level CONTROL command
   (N1's `rename-machine` shape). The agent verifies the grant against its
   pinned owner and persists it under `gid`.
   Minting therefore requires the machine to be online — you are sharing a
   live terminal, so it is.
6. The guest stores the machine as a **guest entry** and attaches normally.

What the relay sees: one pair room (opaque, one-shot, capped) and later an
ordinary attach for `(owner_id, machine_id)` — the guest is indistinguishable
from the owner at the relay. Nothing new to see, store, or rate-limit.

## 3. Enforcement — at the agent, at the PTY

The attach path (`runtime.go` offer handling) gains one branch: if the
offer's binding wallet is not a pinned owner, look up a stored grant for that
wallet. Grant present → verify signature (pinned owner), `nb − 0 ≤ now ≤ na`,
machine matches, gid not tombstoned → Noise-KK against the guest's bound
X25519, then serve **as a guest**:

- **ro**: no PTY shell. The serve loop paints `capture-pane -e -t <scope
  active pane>` once, then streams `pipe-pane -O` bytes as terminal frames.
  Inbound data frames from the guest are read and dropped (resize frames
  accepted, applied only to the guest's own view). FrameWindows is not sent;
  FrameControl is refused outright for guests.
- **rw**: PTY runs D4's grouped launch with the guest name shape
  `guest-<gid[0:8]>` joining the scope session's group, attached with
  `-f ignore-size` semantics D4 already provides. D4's kill guard, sweep,
  and snapshot filter extend to the `guest-` shape.
- **Expiry is a timer, not a hope**: the serve context gets a deadline at
  `na`; expiry kills the PTY/stream with one honest line to the guest
  ("this share ended — ask for a new invite"). Every new attach re-checks the
  clock, the tombstones, and the pinned-owner set.

## 4. Revocation — the agent is the authority

`mir share revoke <gid>` (and the SPA sheet) sends `{a:"revoke-grant",
gid:…}` over an authenticated owner session. The agent tombstones the gid
(persisted), drops any live session serving it, and answers with HELLO — the
same acknowledgement shape as rename. `mir share ls` lists the owner's
locally recorded mints and their expiries.

Deliberately NOT in v1: relay-gossiped grant revocations. The reasoning —
grant revocation only ever concerns one agent; while that machine is
unreachable it cannot serve the guest either, so the revocation has nothing
to race; and the 24 h TTL cap bounds the worst case where the owner never
reconnects to push the tombstone. The existing machine-revocation channel
stays exactly as is. If the field proves this wrong, a `kind`-tagged record
on the same `/revocations` route is the v2 shape (relay change, so it gets
its own review — remember withStatic's signalPaths gotcha, #49).

Interaction with machine retirement (N2): revoking a machine stops *your
devices* trusting it; it does not reach into the machine and kill guests —
the same honesty N2's copy already states ("the machine keeps running").
Retiring a shared machine while a grant lives = power it off or let the
grant's clock run out. BETA.md's sharing section must say this plainly.

## 5. Threat analysis

- **Hostile guest, rw**: arbitrary code execution as the agent's user, full
  tmux server control, network access as that machine. This is the honest
  meaning of sharing a shell. Mitigations are consent-side only: `--write` is
  never default, the mint prompt says "full control of <machine> as your
  user", and the TTL cap bounds it. No pretend sandboxing.
- **Hostile guest, ro**: sees everything that one pane prints while the grant
  lives — including any secret the owner displays. Cannot inject a byte
  (input is dropped agent-side; there is no tmux client to escape — the
  switch-client leak from the measured experiment does not exist because
  nothing interactive is attached). Exfiltration bound = screen content.
- **Stolen invite code**: same profile as a stolen pairing code — one claim
  consumes the room, the owner sees the claimer's wallet + safety number and
  must say yes, and an unclaimed code dies with the room timeout (5 min).
  The owner declining costs the attacker the code.
- **Stolen grant**: bound to `guest` wallet; useless without the guest's
  private key. Public by construction (it transits only the encrypted room
  and the agent's disk, but leaking it costs nothing).
- **Replay / reuse**: the grant is not a session token — every attach runs
  Noise-KK against the guest's key plus a live clock/tombstone check. A
  replayed offer without the guest key fails KK; a replayed grant after
  revocation hits the gid tombstone; after expiry, the clock.
- **Forgery**: Ed25519 over a domain-separated canonical, verified against
  the agent's pinned owner set. The relay cannot mint, alter, or extend a
  grant; neither can a guest (guests hold no owner key; grants are
  non-transferable and guests cannot mint sub-grants).
- **Relay operator**: sees one opaque pair room and ordinary attach metadata
  for the machine — identical to today's traffic. Suppressing traffic denies
  service (already true); it never grants access.
- **Guest's machine is hostile**: equivalent to the guest being hostile;
  scope, mode, and TTL are the whole containment story, as above.
- **Clock games**: the agent's clock decides. An agent with a wildly wrong
  clock mis-enforces TTLs — `mir doctor`'s clock-skew check (N3) already
  warns at 5 min; the share flow's docs point at it.

## 6. UX surface

CLI, owner: `mir share <machine>` (defaults: ro, 1 h, session `main`) →
prints code + QR + web link + "expires 13:45, id 3f2a…"; `--write` prints the
red consequence line and requires typing the machine name to confirm;
`mir share ls`; `mir share revoke <gid>`. Overview (O1): a `s` action on the
selected machine.

CLI, guest: `mir join <code>` → identity-if-needed, safety number, attach.
The machine lands in the store flagged `guest`, with expiry; the overview row
renders "guest · ro · expires in 43 min" and hides rename/retire/share.
`mir ls` shows it the same way. After expiry the row offers only "ask for a
new invite" and a way to forget the entry.

Web: a share sheet on the machine view (mirrors the CLI defaults and the rw
confirm), and the `#join-<code>` route driving §2 with the passkey flow the
SPA already has. A guest's web view is the terminal only — no machine list
beyond their guest entries, no registry fetch, no share minting.

## 7. Slices

| # | Slice | Acceptance |
|---|---|---|
| G1a | Grant record: canonical bytes, sign/verify Go+JS, `testdata/grant.json` vector, TTL/skew rules | Byte-identical vectors gate CI; property tests for validation |
| G1b | `mir share` / `mir join` bootstrap over the pair room; owner confirm; CONTROL `add-grant` | Live share on one machine: mint → join → grant lands on agent; declined SAS pins nothing |
| G1c | Agent enforcement: guest branch in the offer path, ro mirror serve (capture+pipe, input dropped), rw grouped `guest-*`, expiry deadline, tombstones, `revoke-grant` | Netsim-style E2E: ro guest sees output + cannot inject; rw guest types; expiry drops a live guest under 1 s; revoke drops immediately |
| G1d | CLI surface: `share ls/revoke`, guest-flagged store entries, overview integration, copy | House-style copy review; `mir doctor` unaffected |
| G1e | SPA: share sheet, join route, guest terminal view | Phone-first walkthrough of §6 web |
| G1f | Docs: SECURITY.md sharing section, BETA.md, README one-liner | Claims match §5 exactly — no softer, no harder |

Build order G1a → G1b → G1c → G1d → G1e → G1f; G1c is the security core and
gets the adversarial tests before any UX lands.

## Out of scope (v1)

- Guest-of-guest / grant delegation (grants are non-transferable, ever).
- Multi-machine or wildcard-machine grants.
- Follow-the-owner mirroring (re-targeting pipe-pane on window switch via the
  R3 hook machinery — clean v2 if wanted).
- Relay-gossiped grant revocation (§4).
- Guest presence in the owner's overview ("who is watching") — v2 candidate,
  the data exists in `list-clients` + the agent's live grant sessions.

## Open questions for Fredrik

1. **ro = single-pane mirror** is the confinement-honest default, at the cost
   of not following your window switches. Interactive-but-leaky tmux viewing
   was rejected on the measured switch-client leak. OK?
2. **rw stays maximally scary** (type the machine name to confirm, no `--yes`,
   TTL still capped). OK, or should rw not ship in v1 at all?
3. **24 h TTL cap** as the revocation backstop — right number?
4. CLI-first, SPA share sheet in G1e last — or must the phone mint shares
   from day one?

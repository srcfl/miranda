# Naming

**Status: LOCKED → `Miranda` (2026-06-08).** The repo was `terminal-relay` —
descriptive, but it foregrounded the one thing you're *not* supposed to trust (the
relay), and it was generic. (`frood` was briefly the front-runner from the
playful/indie round; reverted in favour of `Miranda`, which carries a stronger,
on-thesis story for a *blind relay*.)

**Why Miranda:** the *Miranda warning* — **"you have the right to remain silent."**
That is precisely what the relay does: it never hears your keystrokes and cannot
testify to what you typed. The product's whole thesis (the relay you don't have to
trust) collapses into one word. It doubles as Shakespeare's *The Tempest* — Miranda
on Prospero the magician's island, *"O brave new world, that has such people in't!"*
— which fits the "definitely magic" tone and opens a thematic name system for the
components.

## Criteria

- Short, memorable, pronounceable; brandable.
- **No collision with a common Unix/CLI command** — we already got burned naming a
  CLI `tr` (it shadowed POSIX `tr`); renamed to `trm`. Don't repeat that.
- Works as a CLI verb: `<name> attach macbook`.
- An **ownable domain** (we lean `.sh` — shell pun + least-crowded namespace; we
  already own `sourceful-labs.net` for the relay).
- Resonates with the vision (terminal everywhere / magic / P2P / don't-panic) and,
  ideally, the Hitchhiker's-Guide tone of the README.

## Round 1 — creative shortlist (47 candidates, 6 angles)

Winner of the round: **Cantrip** — *a tiny spell cast on instinct, no prep* — which
is exactly "type one word, a shell appears." Hitchhiker-adjacent dry wit without
heavy canon, great CLI verb, no Unix collision. Caveat: a *Cantrip* THC-beverage
trademark exists (different class) → trademark check before buying the `.com`.

Runners-up: **Hither** (elegant "toward here" verb, lowest collision risk, slightly
archaic), **Frood** (best Hitchhiker vibe, but close to the real `frodo` Ping CLI),
**Pejl** (Swedish "take a bearing", but two Swedish "Pejl" companies), **Towel**
(most-loved reference, but a common word → weak searchability). Hard rejects flagged
for collisions: **Beeline** (Apache Hive's CLI — the `tr` mistake again), **Tether**
(USDT + an existing dev product), **Babel** (the JS compiler).

## Round 2 — domain-verified (whois on .sh/.dev/.com + npm + GitHub + Unix collision)

The shortlist names had poor domain availability, so we ran a second pass biased to
coined/brandable words and **verified real availability**. 37 names came back with an
available good domain and no Unix collision. Strongest:

| Name | Verified-available | Note |
|---|---|---|
| `porshell` | porshell.sh + .com/.dev/npm/GitHub (full sweep) | "portal to any shell you own"; reads as a word; clean with `.sh` |
| `zappsh` | zappsh.sh + full sweep | instant/encrypted; nods to Sourceful's "Zap"; mild "sh.sh" doubling |
| `termvia` | termvia.sh + .com/.dev/npm | self-documenting: "terminal *via* an untrusted relay"; B2B-serious |
| `spellsh` | spellsh.sh + .com/.dev/npm | "cast a spell" + shell — keeps the Cantrip/magic theme, domains free |
| `warpsh` | warpsh.sh + .com/.dev/npm | best instant decode ("warp into a shell"); but the *Warp* brand is crowded |
| `froodly` | froodly.sh + .dev/npm/GitHub | max Hitchhiker vibe ("a frood who knows where their terminals are") |
| `babelfin` | babelfin.sh + .dev/npm/GitHub | babel-fish metaphor, coined to dodge the `fish` CLI / Babel |
| `zaphy` | zaphy.sh + .dev/npm | Zaphod wink; cross-machine "two heads" energy |

Note: names ending in "sh" read awkwardly with the `.sh` TLD (`…sh.sh`); names that
don't (porshell, termvia, froodly, zaphy, babelfin) pair more cleanly.

## Decision (LOCKED — 2026-06-08)

**Project / brand: `Miranda`.** Angles: the Miranda warning ("right to remain
silent" = the blind relay) as the primary story, *The Tempest* as the literary
backbone.

### CLI tools (scheme A — clean, runnable)

| Role | Binary | Thematic name (story / docs / service brand) |
|---|---|---|
| Client you type all day (`mir attach macbook`) | **`mir`** | Miranda |
| Machine-side agent | **`mir-agent`** | **Prospero** — the magician who conjures the shell |
| Blind signaling / relay server | **`mir-signal`** | **Ariel** — the invisible spirit that carries messages and is bound to obey |

Binaries stay functional (`mir` / `mir-agent` / `mir-signal`) for discoverability;
the Tempest names (Prospero, Ariel) are used in the README, marketing, and as the
brand for the hosted relay service — not as binary names.

### Verification at decision time

- Unix collision: `mir`, `miranda`, `mira` — none on PATH (clean CLI verb).
  (Canonical's "Mir" display server shares the word but is not a CLI binary.)
- Domains: `miranda.sh` / `miranda.io` / `mir.sh` taken (common name); `getmiranda.com`
  free. Not blocking — the product can live under `*.sourceful-labs.net`.
- npm: irrelevant (Go project).
- **Known collisions (accepted):** *Miranda* the functional programming language
  (David Turner, ~1985, a Haskell ancestor) and *Miranda IM* (2000s chat client).
  Both dev-adjacent → some name recognition + weaker SEO. Accepted for the strength
  of the story; distinct enough in context.

### Rollout (mechanical, do as its own change / PR)

- Rename binaries: `trm` → `mir`, `tr-agent` → `mir-agent`, `tr-signal` →
  `mir-signal` (`go/cmd/*`, Makefile, `web/dev-serve.sh`, deploy scripts, systemd unit).
- Rewrite README (SEO + what-it-does first, then story) and update CLAUDE.md + deploy docs.
- Rename repo `srcfl/terminal-relay` → `srcfl/miranda` (GitHub keeps a redirect).
- Re-deploy the relay after the binary rename (`deploy/lightsail/redeploy.sh`).
- Optional: buy a marketing domain (`getmiranda.com` free) or keep `*.sourceful-labs.net`.

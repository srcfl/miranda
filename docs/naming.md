# Naming (work in progress)

The repo is currently `terminal-relay` — descriptive, but it foregrounds the one
thing you're *not* supposed to trust (the relay), and it's generic. We'll likely
rename before the 1.0 release. **Status: deferred — focus on function first.
Current favourite: `Cantrip`. Renaming later is cheap** (rename the binary + repo,
transfer, update docs), so we're not blocking the build on it.

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

## Decision

**Deferred.** Leaning `Cantrip` (best vibe + CLI verb; domain via a verified
alternative or `.dev`/`.sh`). We'll lock the name before 1.0 / before transferring
the repo to the `srcfl` org. For now: build function; the name is a cheap,
reversible change.

# terminal-relay

> **Your terminals, on every machine you own, reachable from anywhere — with no
> SSH keys to juggle, no ports to forward, and a relay you don't have to trust.**
> Mostly harmless. Definitely magic.

---

## The Why (or: a story about juggling)

This was born out of frustration, which — as the Guide notes — is how most useful
things in the universe come to be, the rest being born out of either boredom or a
profound misunderstanding of the laws of thermodynamics.

The specific frustration was this: there is, on any given day, **a small herd of
Claude Code sessions** scattered across a laptop, an office Mac mini, and a Linux
box that exists mainly to be warm. One of them is doing the interesting thing. The
others are also doing the interesting thing, somewhere, probably, and getting to
the right one involved an amount of `ssh`-ing, tunnel-poking, and quiet swearing
that is not, strictly speaking, *magic*.

A few stubborn convictions fell out of that:

- **I love my terminal and I am not leaving.** The terminal isn't a fallback for
  when the GUI breaks. It's the best window we've got. I want to stay in it.
- **The tool must not belong to one robot.** Claude Code, Codex, whatever comes
  next — they're all just things that run *in a terminal*. So don't build for the
  robot. Build for the window. The terminal already is the universal interface;
  it has been since before graphical anything, and (Lindy says) will be long after.
- **Don't reinvent the engine.** `tmux` is good. `tmux` has been good since
  approximately the dawn of time and will be good when we are all dust. We are not
  going to out-clever forty years of session-multiplexing on a Tuesday. We keep
  the proven engine and build *around* it.
- **It should feel like magic** — open a thing on any device, and there are your
  machines, alive, exactly as you left them. The reaction we're after is the one
  where you tilt your head and go *"…wait, why has nobody done this already?"*
- **Modern, not antique.** No password files, no copied keys, no "paste this 4096-bit
  blob into authorized_keys." **Passkeys.** Real end-to-end crypto. The newest safe
  web tech, used properly.
- **As serverless as physics allows.** There's a relay, because two machines behind
  two NATs need an introduction. But it's a *blind matchmaker* — it never sees your
  traffic, and you never have to trust it (see below). One day the relay itself
  could be decentralized. The Guide files that under *Improbable, But Not As
  Improbable As You'd Think*.

So: **SSH, without thinking about SSH.** A terminal that exists on every device.
Peer-to-peer, end-to-end encrypted, passkey-shaped, tmux-powered. The number we
were aiming for, naturally, was 42.

---

## Don't Panic (the trust bit, which is the whole point)

There's a relay. **You don't have to trust it.**

Your terminal traffic flows **peer-to-peer**, end-to-end encrypted (Noise `KK`).
The relay only introduces the two ends and then gets out of the way — it sees
ciphertext and routing metadata, never your keystrokes, never your output, and it
*cannot* impersonate you or your machines. At pairing time both ends show a
**safety number**; if they match, you've seen with your own eyes that no one is in
the middle.

The exact, falsifiable model — what the relay can and can't do, what you *do* have
to trust, and how to verify all of it — lives in **[`SECURITY.md`](SECURITY.md)**.
That document is not an afterthought. It is the project.

---

## Status (honest, per the trust ethos)

- ✅ **Works today:** the `trm` CLI — pair a machine with one code/QR, attach to a
  real shell over P2P, multiplex across all your machines, persistent `tmux`
  sessions. A hosted relay is live at `relay.sourceful-labs.net`.
- 🚧 **Coming:** the browser client (passkeys + WebRTC + xterm.js → from your
  phone), one-line install, signed releases, a third-party audit, and (one day) a
  decentralized relay.

## Quickstart

```bash
# build the CLIs (Go 1.26+; tmux for persistence)
make install                 # -> ~/.local/bin: trm, tr-agent, tr-signal

# on a machine you want to reach:
tr-agent pair               # prints a pairing code + QR, then waits
tr-agent up &               # run the agent (persistent tmux sessions)

# on your client (laptop, another machine):
trm pair <code>             # ...the code from above; compare the safety numbers
trm attach <machine>        # you're in. a real shell, over P2P.
```

Several machines at once — the cross-machine multiplexer:

```bash
trm attach laptop macmini linux
# Ctrl-O then 1-9 / n / q to switch machines (change the key with --prefix)
```

Everything defaults to our relay + STUN, so no flags are needed. Point at your own
infrastructure with `--signal` / `TR_SIGNAL` and `--stun` / `TR_STUN`.

Security migration: current agents auto-add a local `registration_secret` to
`config.json` on the next `tr-agent enroll`, `tr-agent pair`, or `tr-agent up`.
Restart long-running agents after updating. Relays still accept older no-secret
agents until a proof has been learned for that `owner_id` + `machine_id`; after
that, replacements must present the same proof.

## How it works (the short version)

```
  your client            relay (blind matchmaker)         your machine
  ┌───────────┐  WSS: who/where (SDP)  ┌────────┐  WSS   ┌──────────────┐
  │ trm        │ ─────────────────────▶│ signal │◀────── │ tr-agent      │
  │ + Noise    │ ◀─────────────────────│no data │ ─────▶ │ + Noise + tmux│
  └─────┬──────┘                        └────────┘        └──────┬───────┘
        └════════ WebRTC DataChannel (direct P2P) ═══════════════┘
                  Noise KK runs INSIDE it — the relay only sees ciphertext
```

- **Identity** is a passkey-derived key (browser) or a local key (CLI). The relay
  never holds your private key.
- **Pairing** uses a one-time token (the QR/code) as the PSK of a Noise `NNpsk0`
  handshake; the relay only ever sees `hash(token)`.
- **Per machine:** `tmux` — persistence, windows, panes; the Lindy-approved engine.
- **Across machines:** the `trm` client switches focus. tmux owns a machine's
  windows; `trm` owns *which machine*. No tmux-in-tmux.

## Repo layout

| Path | What |
|---|---|
| `go/internal/noise` | Noise `KK` (Go + JS interop vectors) |
| `go/internal/pairing` | NNpsk0 one-tap pairing + safety number |
| `go/internal/signal` | the blind signaling/matchmaking server |
| `go/internal/peer` | WebRTC P2P DataChannel glue |
| `go/internal/agent`, `go/cmd/tr-agent` | the machine-side agent |
| `go/internal/client`, `go/cmd/trm` | the `trm` CLI multiplexer |
| `deploy/lightsail` | how the hosted relay is deployed |
| `deploy/netsim` | Docker harness that reproduces real NAT traversal |
| `docs/superpowers/` | design spec + implementation plans |

## Build & test

```bash
cd go && go test ./...          # all green; -race clean
cd deploy/netsim && ./run.sh    # NAT traversal, locally (TURN=1 for the relay path)
```

## License

MIT — see [`LICENSE`](LICENSE).

---

*The Guide also says, of the terminal: "It is the only known interface in the
galaxy that has never once been improved by adding a second monitor."* That last
part may be apocryphal.

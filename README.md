# Miranda

[![Release](https://img.shields.io/github/v/release/srcfl/miranda?sort=semver&color=000000&label=release)](https://github.com/srcfl/miranda/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-black.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/macOS%20%C2%B7%20Linux-black)](#install)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go/go.mod)
[![Data plane](https://img.shields.io/badge/data%20plane-P2P%20%2B%20Noise%20KK-9cf)](#how-it-works)

**SSH-less, peer-to-peer, end-to-end-encrypted shells on every machine you own** — reach a
real terminal from your laptop or your phone's browser, authenticated by a passkey, brokered
by a relay you **never have to trust**.

<p align="center">
  <img src="assets/miranda-demo.gif" width="900"
    alt="mir up serves a machine; from a laptop, mir list auto-discovers it by name and mir attach drops into its real shell — peer-to-peer, Noise-encrypted end-to-end, no SSH">
</p>

<p align="center"><sub>Serve a machine, then reach its real shell from anywhere — P2P + Noise, the relay never sees a keystroke.</sub></p>

Terminal traffic flows **directly peer-to-peer** over a WebRTC DataChannel, end-to-end
encrypted with the Noise `KK` handshake. A small relay only introduces the two ends and then
steps aside — it sees ciphertext and routing metadata, never your keystrokes or output. The
relay, in other words, has **the right to remain silent**.

It's `tmux` for terminals that live on different machines: tmux owns the windows/panes and
persistence on each host; Miranda owns *which machine you're looking at* — and your machines
**find each other by wallet**, so a new one shows up everywhere the moment it comes online.

## Features

- **Peer-to-peer terminal** — direct WebRTC DataChannel, no server in the data path.
- **End-to-end encrypted** — Noise `KK` (X25519 / ChaCha20-Poly1305) runs *inside*
  the channel, so even a malicious relay can't read or MITM your session.
- **Passkey authentication** — WebAuthn `prf` derives your identity. No SSH keys, no
  passwords, no `authorized_keys`. Your passkey is the only thing you carry.
- **Persistent sessions** — `tmux` on each machine. Disconnect and reconnect resume
  the exact same shell, scrollback intact. Long-running agents (Claude Code, Codex)
  survive the browser sleeping or the network dropping.
- **Cross-machine multiplexer** — attach several machines at once and switch focus
  with a hotkey.
- **Zero-config discovery** — your machines self-publish a wallet-signed, *encrypted*
  record; every device of yours lists them by name automatically — no `add-machine`, no
  pairing between your own machines. The relay holds only opaque blobs it can't read.
- **LAN-direct** — on the same network, `mir attach` finds the machine over mDNS and
  connects straight over QUIC: no relay round-trip at all, automatic fallback if it's remote.
- **Browser or CLI** — the `mir` CLI in your terminal, or any browser including
  iPhone Safari.
- **Self-hostable, blind relay** — run your own, or use the hosted one; either way it
  never sees plaintext.
- **macOS + Linux**, single static Go binaries. MIT licensed.

## Install

Prebuilt binaries for macOS and Linux are published on GitHub Releases. Install the
client with one line:

```bash
# client (mir)
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh

# agent on a machine you want to reach
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh -s -- --agent
```

The installer verifies each download against the release `checksums.txt` before
installing to `~/.local/bin`. Pin a version with `MIR_VERSION=v0.6.0`, or change the
target with `INSTALL_DIR=/usr/local/bin`. Prefer building from source? See the
[Quickstart](#quickstart) below.

## Quickstart

```bash
# build the CLIs (Go 1.26+; tmux for persistence)
git clone https://github.com/srcfl/miranda && cd miranda
make install                 # -> ~/.local/bin: mir, mir-agent, mir-signal

# on a machine you want to reach (same `mir` binary — every node is symmetric):
mir pair                     # prints a pairing code + QR, then waits
mir up &                     # serve this machine (persistent tmux sessions)

# on your client (laptop, another machine):
mir pair <code>              # ...the code from above; compare the safety numbers
mir attach <machine>         # you're in. a real shell, over P2P.
```

Several machines at once — the cross-machine multiplexer:

```bash
mir attach laptop macmini linux
# Ctrl-O then 1-9 / n / q to switch machines (change the key with --prefix)
```

Everything defaults to the hosted relay + STUN, so no flags are needed. Point at your
own infrastructure with `--signal` / `MIR_SIGNAL` and `--stun` / `MIR_STUN`.

**Your machines appear automatically.** Once they share your wallet (passkey-sync, or
`mir wallet import-phrase` on a new machine), `mir up` publishes an **encrypted** record to
the relay and your machines show up by name in `mir list` and the browser — no
`mir add-machine`, no pairing between your own devices. The relay only ever holds opaque
blobs it can't read; only your wallet decrypts them, and a forged record simply fails to
open. A new machine prints a one-line "new device joined" notice. It's online-discovery:
a powered-off machine reappears when it comes back; to retire one, turn it off (or, if a
device is compromised, rotate with `mir keygen --wallet`).

**LAN-direct (no relay on the same network).** When the client and the machine are on
the same LAN, `mir attach` finds it over mDNS and connects straight over QUIC — no relay
round-trip. It's automatic and falls back to the relay within ~0.6 s if there's no local
answer. Same trust as ever: Noise-KK + the wallet binding run inside, so the LAN only
supplies an address. Turn it off with `mir up --no-lan` (agent) or
`mir attach --relay-only` (client).

## Updating

`mir` checks GitHub for a newer release at most once a day and prints a one-line
notice — never blocking your command. Apply it when you choose:

```bash
mir self-update
```

Disable the check with `MIR_NO_UPDATE_CHECK=1`. For unattended machines, opt into
automatic updates — applied only when no session is active, then the agent re-execs
in place so its PID (and any systemd wrapper) survives:

```bash
mir up --auto-update            # or MIR_AUTO_UPDATE=1
```

> `mir-agent` is a deprecated alias for `mir` and forwards to it (with a notice). Use
> `mir` everywhere; the shim will be removed in a future release.

## Don't trust the relay — that's the whole point

There's a relay, because two machines behind two NATs need an introduction. **You
don't have to trust it.**

Your terminal traffic flows **peer-to-peer**, end-to-end encrypted (Noise `KK`). The
relay only brokers the WebRTC offer/answer + ICE candidates that let the two ends
find each other, then gets out of the way. It learns only `owner_id`, `machine_id`,
the SDP/ICE blobs, and liveness metadata — never your keystrokes, never your output,
and it *cannot* impersonate you or your machines. At pairing time both ends show a
**safety number**; if they match, you've seen with your own eyes that no one is in
the middle.

The exact, falsifiable model — what the relay can and can't do, what you *do* have to
trust, and how to verify all of it — lives in **[`SECURITY.md`](SECURITY.md)**. That
document is not an afterthought. It is the project.

## How it works

```
  your client            relay (blind matchmaker)         your machine
  ┌───────────┐  WSS: who/where (SDP)  ┌────────┐  WSS   ┌──────────────┐
  │ mir        │ ─────────────────────▶│ signal │◀────── │ mir-agent     │
  │ + Noise    │ ◀─────────────────────│no data │ ─────▶ │ + Noise + tmux│
  └─────┬──────┘                        └────────┘        └──────┬───────┘
        └════════ WebRTC DataChannel (direct P2P) ═══════════════┘
                  Noise KK runs INSIDE it — the relay only sees ciphertext
```

- **Identity** is a passkey-derived key (browser) or a local key (CLI). The relay
  never holds your private key.
- **Pairing** uses a one-time token (the QR/code) as the PSK of a Noise `NNpsk0`
  handshake; the relay only ever sees `hash(token)`.
- **Per machine:** `tmux` — persistence, windows, panes; the proven engine.
- **Across machines:** the `mir` client switches focus. tmux owns a machine's
  windows; `mir` owns *which machine*. No tmux-in-tmux.

The components, in *The Tempest*'s cast: the agent on each machine is **Prospero**
(the magician who conjures the shell), and the hosted relay is **Ariel** (the
invisible spirit that carries messages and can't speak of what it carries).

## Status

- ✅ **Works today:** the `mir` CLI — pair a machine with one code/QR, attach to a
  real shell over P2P, multiplex across all your machines, persistent `tmux`
  sessions. A hosted relay is live at `relay.sourceful-labs.net`.
- 🚧 **Coming:** the browser client (passkeys + WebRTC + xterm.js → from your phone),
  signed releases (cosign), a third-party audit, and (one day) a decentralized relay.

> **Ops note:** agents auto-add a local `registration_secret` to `config.json` on the
> next `mir enroll`, `mir pair`, or `mir up`. Restart long-running
> agents after updating. Relays accept older no-secret agents until a proof has been
> learned for that `owner_id` + `machine_id`; after that, replacements must present
> the same proof.

## The story (or: a tale about juggling)

This was born out of frustration, which — as a certain Guide notes — is how most
useful things come to be, the rest being born of either boredom or a profound
misunderstanding of the laws of thermodynamics.

The specific frustration: on any given day there is **a small herd of Claude Code
sessions** scattered across a laptop, an office Mac mini, and a Linux box that exists
mainly to be warm. One of them is doing the interesting thing. The others are also
doing the interesting thing, somewhere, probably, and getting to the right one
involved an amount of `ssh`-ing, tunnel-poking, and quiet swearing that is not,
strictly speaking, *magic*.

A few stubborn convictions fell out of that:

- **I love my terminal and I am not leaving.** The terminal isn't a fallback for when
  the GUI breaks. It's the best window we've got. I want to stay in it.
- **The tool must not belong to one robot.** Claude Code, Codex, whatever comes next —
  they're all just things that run *in a terminal*. So don't build for the robot.
  Build for the window. The terminal has been the universal interface since before
  graphical anything, and will be long after.
- **Don't reinvent the engine.** `tmux` is good, has been good since approximately the
  dawn of time, and will be good when we are all dust. We keep the proven engine and
  build *around* it.
- **It should feel like magic** — open a thing on any device, and there are your
  machines, alive, exactly as you left them. The reaction we're after is the one
  where you tilt your head and go *"…wait, why has nobody done this already?"*
- **Modern, not antique.** No password files, no copied keys. **Passkeys.** Real
  end-to-end crypto. The newest safe web tech, used properly.
- **As serverless as physics allows.** There's a relay, because two NATs need an
  introduction. But it's a *blind matchmaker* — it never sees your traffic, and you
  never have to trust it. One day the relay itself could be decentralized.

The name is **Miranda** — for the Miranda warning ("you have the right to remain
silent"), which is exactly what a blind relay does, and for Shakespeare's Miranda on
the magician's island, who looks out at a connected world and says *"O brave new
world, that has such people in't!"* (See [`docs/naming.md`](docs/naming.md) for the
full naming rationale.)

So: **SSH, without thinking about SSH.** A terminal that exists on every device.
Peer-to-peer, end-to-end encrypted, passkey-shaped, tmux-powered.

## Repo layout

| Path | What |
|---|---|
| `go/internal/noise` | Noise `KK` (Go + JS interop vectors) |
| `go/internal/pairing` | NNpsk0 one-tap pairing + safety number |
| `go/internal/signal` | the blind signaling/matchmaking server |
| `go/internal/peer` | WebRTC P2P DataChannel glue |
| `go/internal/agent`, `go/cmd/mir-agent` | the machine-side agent |
| `go/internal/client`, `go/cmd/mir` | the `mir` CLI multiplexer |
| `go/cmd/mir-signal` | the signaling/relay server |
| `web/` | browser client (vanilla JS + xterm.js) |
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

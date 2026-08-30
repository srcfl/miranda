# Miranda

[![Release](https://img.shields.io/github/v/release/srcfl/miranda?sort=semver&color=000000&label=release)](https://github.com/srcfl/miranda/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-black.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/macOS%20%C2%B7%20Linux-black)](#install)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go/go.mod)

**Leave your desk. Keep your terminal.**

Miranda is the reach layer for persistent terminals. A multiplexer keeps your
session alive **on the machine**. Miranda gets you **to that machine** — from
whatever device is in your hand, with a passkey. No VPN, no copied SSH key, no
open inbound port, no account.

Start an agent or a build in `tmux` on your workstation, walk away, and pick up
the same live terminal on a laptop or a phone.

We do not build a multiplexer; we make yours reachable. Miranda connects **you to
persistent terminals on machines you own** — not a VPN, network overlay, remote
desktop, or multi-user access platform.

<p align="center">
  <img src="assets/miranda-demo.gif" width="900"
    alt="Miranda reconnecting to a persistent terminal session on another machine">
</p>

## Beta

Miranda is in public beta. Read [BETA.md](BETA.md) for what works today, the
known gaps (passkey/browser matrix, NAT numbers, external audit — none
published yet), and how to report a problem.

## Is it “P2P tmux”? How is it different from a multiplexer?

The useful boundary is the layer:

- A multiplexer — `tmux`, or an agent-aware one like
  [herdr](https://github.com/herdrdev/herdr) — keeps sessions, panes, windows,
  and the processes inside them alive **on one machine**.
- Miranda keeps that machine **reachable from the device in your hand**: passkey
  identity, pairing, discovery, NAT traversal, encrypted resume, and time-boxed
  sharing.

So Miranda does not replace your multiplexer. It makes the sessions you already
run follow you. tmux is the engine it drives today; carrying other engines is
the direction, tracked in [#107](https://github.com/srcfl/miranda/issues/107),
not something that works yet.

| Tool | Its job | What Miranda adds |
|---|---|---|
| `tmux`, herdr | Keep a session alive on one machine | Reach and resume it from another device |
| SSH | Log in to a host you can already route to | Passkey-first pairing, discovery, browser and phone access, no exposed SSH service |
| VPN/overlay | Put devices on one private network | Expose one terminal, not the rest of the network |
| Miranda | Get you to your own live terminals | The focused product |

## The one-minute flow

On the machine whose terminal should stay available:

```bash
mir pair       # shows a QR/code and a six-group safety number
# scan with the Miranda web app, or run `mir pair <code>` on a client
# compare and confirm the safety number on both ends
mir up         # keeps the machine's tmux session reachable
```

Then, from another terminal:

```bash
mir list
mir attach workstation
```

Or open the Miranda web app on a passkey-capable phone or laptop. Machines paired
to the same owner appear by name from an end-to-end-encrypted registry.

Attach several machines at once with `mir attach a b c`; press `Ctrl-O`, then
`1`–`9` or `n`, to switch focus.

## Five claims you can check

- **Your passkey is the identity, and there is no account.** No signup, no
  password, no user record. The owner identity is derived from the passkey PRF
  output, so the browser re-derives it for each session and stores no private
  key; the relay and every machine you pair hold public IDs only. Pair once,
  compare one six-group safety number, and never touch `authorized_keys` again.
  → [`go/internal/identity/owner.go`](go/internal/identity/owner.go),
  [B1 identity spec](docs/superpowers/specs/2026-06-11-b1-wallet-identity.md)
- **The relay cannot read, and keeps next to nothing.** Terminal bytes run
  through Noise `KK` inside the WebRTC DataChannel, so the relay sees `owner_id`,
  `machine_id`, and routing metadata. Discovery records are owner-encrypted blobs
  it never opens, held only while your agent is online — a restart loses every
  one. The single thing it persists is an owner-signed revocation.
  → [`go/internal/signal/server.go`](go/internal/signal/server.go),
  [SECURITY.md](SECURITY.md#components-and-trust)
- **Direct by default, and measured.** One connection, two ICE modes: peers pair
  directly on a LAN or hole-punch across the internet, and TURN forwards
  ciphertext only where that fails. In the Docker NAT matrix, attach takes 12 ms
  on a LAN host pair, 17 ms to an openly reachable agent, and ~1.0 s when only
  TURN can work; a Wi-Fi→cellular flip resumes in 2.35 s direct, 2.77 s over
  TURN. `make netsim` reruns the whole matrix on your laptop.
  → [`netsim/results/results.md`](netsim/results/results.md)
- **Sharing with an expiry date.** `mir share` mints an owner-signed grant naming
  one machine, one guest key, and a window — 1 h by default, 24 h hard cap. It is
  read-only unless you say otherwise, and the agent enforces that by dropping
  guest input, with no tmux client for a guest to escape. Revocable, no accounts,
  no third party.
  → [SECURITY.md](SECURITY.md#session-sharing),
  [G1 sharing design](docs/superpowers/specs/2026-08-30-g1-guest-sharing-design.md)
- **The gaps are written down too.** Release binaries are reproducible and
  cosign-signed, the installer fails closed without a valid signature, the threat
  model is published, and so is the list of what Miranda does not do and has not
  proved.
  → [`docs/release.md`](docs/release.md), [SECURITY.md](SECURITY.md),
  [audit scope](docs/audit-scope.md)

## Security in one screen

Miranda assumes the rendezvous relay is untrusted.

1. Pairing uses a random 128-bit token as a Noise `NNpsk0` PSK. Both sides show a
   96-bit safety number before trust is persisted.
2. The passkey-holding client signs the agent's registration commitment during
   pairing. A third party cannot squat an unused `owner_id` + `machine_id` slot.
3. Every remote attach must carry a fresh owner signature bound to the relay-issued
   session, machine ID, and exact SDP offer. The agent verifies it before allocating
   ICE, TURN, or a peer connection.
4. Terminal bytes run through mutually authenticated Noise `KK` inside the
   WebRTC DataChannel. Transport encryption is not the identity boundary.
5. The target agent stores its host key, a registration secret, pinned owner
   **public** IDs, and owner-encrypted registry blobs. It never stores the owner's
   root or private key.

The relay can observe routing metadata, candidate IP addresses, machine IDs, and
timing. It can deny service. It cannot decrypt terminal data or complete the pinned
Noise handshake as either endpoint. The browser application origin is a client-code
trust root, just like an installed binary.

Read the exact guarantees, non-goals, and residual risks in
[SECURITY.md](SECURITY.md).

## Install

Miranda currently targets macOS and Linux. `tmux` is required on machines serving
persistent sessions. Release installation requires `cosign` and fails
closed unless the checksum manifest has a valid keyless signature from this
repository's tagged release workflow. Native owner clients store their root in
macOS Keychain or Linux Secret Service; on Linux, install the package that provides
`secret-tool` (`libsecret-tools` on Debian/Ubuntu). Target-only agents do not need
keychain access.

```bash
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh
```

The binary is installed to `~/.local/bin` by default. Set `MIR_VERSION` to pin a
release or `INSTALL_DIR` to change the destination.

After installation, run `mir doctor`. It checks state separation and permissions,
the keychain reference, tmux, signed revocations, and relay health without printing
private material.

To build the current source instead:

```bash
git clone https://github.com/srcfl/miranda
cd miranda
make install
```

The same `mir` binary is used on clients and target machines. `mir-agent` remains
only as a deprecated compatibility shim.

## Self-hosting and local development

The defaults point at the project's hosted web and signaling endpoints. They are a
convenience, not part of the cryptographic trust boundary or an availability SLA.
Override them with:

```bash
MIR_SIGNAL=https://relay.example mir up
MIR_SIGNAL=https://relay.example MIR_WEB=https://term.example mir pair
MIR_STUN=stun:stun.example:3478 mir attach workstation
```

Build and run a local relay with:

```bash
make build
./bin/mir-signal --addr :8443 --webroot ./web
```

Deployment examples live under [`deploy/`](deploy/).

To see how attach and reconnect behave behind real NATs without leaving your
laptop, `make netsim` puts an agent and a client behind separate simulated NATs
in Docker and measures attach, TURN fallback and resume after a network flip. The
current numbers are in
[`netsim/results/results.md`](netsim/results/results.md); the topology and what
each NAT approximates are in [`netsim/README.md`](netsim/README.md).

## What Miranda intentionally does not do

- replace your terminal multiplexer, or ship one of its own;
- route IP packets, subnets, databases, or arbitrary TCP services;
- act as an SSH server or support the SSH wire protocol;
- transfer or synchronize files;
- provide organization roles, shared accounts, session recording, or approval
  workflows;
- protect a compromised endpoint, browser origin, or passkey account;
- claim independent security validation yet.

The first one is the whole position, not an apology: keeping a session alive is a
solved job, and Miranda is the layer that gets you to it. A small capability is
easier to understand, safer to grant, and easier to make feel magical.
The longer positioning decision is in [`docs/product.md`](docs/product.md).

## Status

Miranda is **v0.7.0**. The CLI and browser can pair,
discover, attach, reconnect, multiplex machines, and resume real tmux sessions over
the P2P data plane. Machine revocation, native OS-keychain storage, bounded
relay rate limits, diagnostics, signed releases, and reproducible binary checks are
implemented. Go and browser crypto are gated by shared interop vectors.

It is not independently audited or production-certified. The remaining gates are
external audit, measured hostile public-relay/NAT testing, and validation of the
actual hosted configuration against the runbooks. See [SECURITY.md](SECURITY.md)
for the honest boundary.

### v0.7 migration

This security reset is intentionally not a silent upgrade from the old
wallet-shaped model:

- state is split into `~/.miranda/client` and `~/.miranda/agent`;
- agents no longer receive or retain an owner root;
- the neutral Miranda signer produces a new owner ID;
- agent registration now requires an owner authorization created during pairing;
- native owner roots migrate from `owner.json` into macOS Keychain/Linux Secret
  Service, with no plaintext fallback;
- revoked machines are permanent owner-signed tombstones;
- `mir identity` replaces the old wallet commands.

Keep a backup of old state, install the new version on both ends, and pair each
machine again. Do not copy an old combined state directory into either new path.

## Architecture

```text
 passkey / mir client       blind rendezvous relay          target machine
 ┌──────────────────┐      ┌──────────────────────┐       ┌─────────────────┐
 │ owner identity   │ WSS  │ SDP/ICE + metadata   │  WSS  │ host identity   │
 │ registry key     │─────▶│ no terminal plaintext│◀──────│ tmux + PTY       │
 └────────┬─────────┘      └──────────────────────┘       └────────┬────────┘
          └═══════ WebRTC DataChannel, Noise KK E2E (direct/TURN) ═══┘
```

The cryptographic wire domains and Go module path retain their historical
`terminal-relay` strings for compatibility; the product and binaries are Miranda.

## Repository

| Path | Purpose |
|---|---|
| [`go/internal/noise`](go/internal/noise) | Noise `KK` transport and Go interop |
| [`go/internal/pairing`](go/internal/pairing) | Noise `NNpsk0` pairing and provisioning |
| [`go/internal/identity`](go/internal/identity) | domain-separated owner identity and proofs |
| [`go/internal/signal`](go/internal/signal) | blind rendezvous and encrypted registry |
| [`go/internal/agent`](go/internal/agent) | attach authorization, PTY, tmux, runtime |
| [`web`](web) | passkey browser client and xterm.js UI |
| [`netsim`](netsim) | Docker NAT matrix: measured attach, TURN fallback, resume |
| [`testdata`](testdata) | stable cross-language cryptographic vectors |

```bash
cd go && go test ./...
cd ../web && npm test
```

MIT — see [LICENSE](LICENSE). Security reports: security@sourceful-labs.net.

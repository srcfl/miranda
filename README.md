# Miranda

[![Release](https://img.shields.io/github/v/release/srcfl/miranda?sort=semver&color=000000&label=release)](https://github.com/srcfl/miranda/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-black.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/macOS%20%C2%B7%20Linux-black)](#install)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go/go.mod)

**Leave your desk. Keep your terminal.**

Miranda is passkey-native terminal continuity for long-running development and
AI sessions. Start work in `tmux` on one of your machines, walk away, and continue
the same live terminal from a laptop or phone. No inbound port forwarding, copied
SSH keys, or blanket network access.

It is deliberately narrow: Miranda connects **you to persistent terminals on
machines you own**. It is not a general VPN, network overlay, remote desktop, or
multi-user access platform.

<p align="center">
  <img src="assets/miranda-demo.gif" width="900"
    alt="Miranda reconnecting to a persistent terminal session on another machine">
</p>

## Beta

Miranda is in public beta. Read [BETA.md](BETA.md) for what works today, the
known gaps (passkey/browser matrix, NAT numbers, external audit — none
published yet), and how to report a problem.

## Is it “P2P tmux”?

Close, but the useful boundary is:

- `tmux` owns persistence, panes, windows, and processes **inside one machine**.
- Miranda owns passkey identity, machine pairing, discovery, reachability, and
  encrypted continuity **between your devices and machines**.

So the exact niche is a **private terminal-continuity mesh**. Miranda does not
replace tmux; it makes your tmux sessions securely follow you.

| Tool | Its job | What Miranda adds |
|---|---|---|
| `tmux` | Keep a local terminal session alive | Reach and resume it from another device |
| SSH | Log in to a host | Passkey-first pairing, discovery, browser access, no exposed SSH service |
| VPN/overlay | Put devices on one private network | Expose only a terminal, not the rest of the network |
| Miranda | Continue your own live terminals | The focused product |

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

## Why it feels simpler

- **One terminal-shaped capability.** Miranda does not grant subnet access or
  expose unrelated services.
- **Passkey-first browser identity.** WebAuthn PRF derives the owner identity for
  the current ceremony. The owner private key is not stored by the agent or relay.
- **One visual trust step.** Pair once, compare one safety number, then reconnect
  without managing `authorized_keys`.
- **Persistent by default.** Network changes, browser sleep, or closing the lid do
  not kill the work running inside tmux.
- **Direct when possible.** WebRTC connects peer-to-peer across networks; LAN
  clients use direct QUIC. An optional TURN server can forward ciphertext where
  direct NAT traversal fails.
- **Blind discovery.** The relay stores only owner-encrypted machine records while
  agents are online.

## Security in one screen

Miranda assumes the rendezvous relay is untrusted.

1. Pairing uses a random 128-bit token as a Noise `NNpsk0` PSK. Both sides show a
   96-bit safety number before trust is persisted.
2. The passkey-holding client signs the agent's registration commitment during
   pairing. A third party cannot squat an unused `owner_id` + `machine_id` slot.
3. Every remote attach must carry a fresh owner signature bound to the relay-issued
   session, machine ID, and exact SDP offer. The agent verifies it before allocating
   ICE, TURN, or a peer connection.
4. Terminal bytes run through mutually authenticated Noise `KK` inside WebRTC or
   QUIC. Transport encryption is not the identity boundary.
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

## What Miranda intentionally does not do

- route IP packets, subnets, databases, or arbitrary TCP services;
- act as an SSH server or support the SSH wire protocol;
- transfer or synchronize files;
- provide organization roles, shared accounts, session recording, or approval
  workflows;
- protect a compromised endpoint, browser origin, or passkey account;
- claim independent security validation yet.

Those constraints are the product strategy. A small capability is easier to
understand, safer to grant, and easier to make feel magical.
The longer positioning decision is in [`docs/product.md`](docs/product.md).

## Status

Miranda is **v0.7.0**. The CLI and browser can pair,
discover, attach, reconnect, multiplex machines, and resume real tmux sessions over
the P2P/LAN data plane. Machine revocation, native OS-keychain storage, bounded
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
          └════ WebRTC DataChannel or LAN QUIC, Noise KK E2E ══════┘
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
| [`testdata`](testdata) | stable cross-language cryptographic vectors |

```bash
cd go && go test ./...
cd ../web && npm test
```

MIT — see [LICENSE](LICENSE). Security reports: security@sourceful-labs.net.

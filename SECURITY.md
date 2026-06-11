# Security model

Miranda lets you reach a real shell on your machines from anywhere. The
whole point is that **you do not have to trust the relay** (our hosted signaling
server, the Cloudflare proxy in front of it, an optional TURN relay, or the
network in between). This document states precisely what that means, what you
_do_ have to trust, and how to verify the claims.

We do not say "100% secure" — no system is. We make a **precise, falsifiable
guarantee** and are honest about the residual exposure. The honesty is the point:
if you can read this and audit the code, you can decide for yourself.

## The guarantee

> The relay **cannot read or modify your terminal traffic**, and **cannot
> impersonate you or your machines**. Terminal data flows peer-to-peer and is
> end-to-end encrypted; the relay only ever sees ciphertext and routing metadata.

This holds even if the relay operator is malicious, the relay is compromised, or
the network is hostile — provided the trust roots below are intact.

## How it works (the basis for the guarantee)

- **Peer-to-peer data plane.** Terminal bytes travel over a direct WebRTC
  DataChannel between your client and the agent (hole-punched via STUN). They do
  **not** pass through the relay. The relay only brokers the WebRTC handshake
  (SDP/ICE) and then steps out. (See `go/internal/peer`, `go/internal/signal`.)
- **End-to-end encryption: Noise `KK`.** Inside the DataChannel we run
  `Noise_KK_25519_ChaChaPoly_SHA256` — mutual authentication with **pinned static
  keys** plus forward secrecy. Because both ends already hold each other's static
  public key (pinned at pairing), a relay that tampers with the WebRTC DTLS
  fingerprints (a classic proxy MITM) **cannot** complete the handshake. DTLS is
  just transport; Noise is what authenticates the peers. (See `go/internal/noise`,
  certified byte-for-byte against the reference implementation in `testdata/`.)
- **Identity.** Your **owner key** is derived from your passkey (WebAuthn `prf`)
  in the browser, or a local key file for the CLI. The relay never sees the
  private key, so it can never authenticate as you to an agent. Each agent has a
  **host key** pinned by you at pairing (trust-on-first-use).
- **Pairing without trusting the relay.** Adding a machine uses a one-time,
  128-bit **token** shown out-of-band (a QR/code you scan or paste). The token is
  the pre-shared key of a `Noise_NNpsk0` handshake; the relay only ever sees
  `roomID = H(token)` (domain-separated) and opaque ciphertext — never the token,
  never the exchanged keys. So the relay cannot MITM pairing either. The token is
  the **trust anchor**: it is the one secret that bootstraps everything, and it
  travels out of band, not through the relay. (See `go/internal/pairing`.)
- **Agent registration proof.** Each agent persists a random
  `registration_secret` in its local `config.json` and sends it only on
  `/agent/signal` as `X-TR-Agent-Registration-Secret`. The relay learns that
  proof for the `owner_id` + `machine_id` slot and rejects later replacement
  attempts that do not present the same value. This protects live registrations
  from clients that only know routing metadata while keeping the relay blind to
  terminal bytes, host private keys, owner secrets, and pairing tokens. Existing
  configs are auto-migrated on the next agent load; older no-secret agents keep
  legacy behavior until a relay has learned a proof for that slot.

## What you have to trust (and it is never the relay)

1. **The pairing token, at the one moment it is shown.** Treat it as a bearer
   secret: scan the QR in person or paste it over a channel you trust. It is
   single-use and short-lived. If an attacker obtains a live token, they could
   pair in your place — so do not post it publicly.
2. **Your passkey / iCloud Keychain** (browser) or your **owner key file** (CLI).
   Whoever holds these is you.
3. **The target machine.** The agent runs a real shell as you; a compromised host
   is game over (that is true of SSH too). **Run the agent as a dedicated,
   low-privilege user — never as root.** The shell it spawns inherits the agent's
   privileges, so anyone who reaches that shell gets exactly that user's rights;
   running as root turns a single agent compromise (or a leaked pairing token)
   into full control of the box. Give it its own unprivileged account, no
   passwordless sudo, and only the access that account genuinely needs.
4. **The code you run.** This is why open source + verifiable builds matter (see
   roadmap). Do **not** install binaries fetched blindly from the relay.
5. **The browser JavaScript served by `term.sourceful-labs.net`.** When the SPA is
   served from `mir-signal`, that JavaScript is a trust root just like an installed
   binary. A compromised Cloudflare zone, origin host, deploy key, or build output
   could ship client code that reads terminal data before encryption or derives
   the owner key after a valid WebAuthn ceremony.

## Residual exposure (honest, by design)

- **Metadata.** The relay sees: your `owner_id` (a pseudonymous public key, not
  your name), your `machine_id`s, the SDP/ICE blobs (**which include your
  machines' candidate IP addresses** — inherent to establishing a direct path),
  and connection timing. It does **not** see terminal content, display names, or
  what you run. If hiding IPs from the relay matters to you, run over a VPN/overlay.
- **Availability.** The relay is the rendezvous point; a malicious or down relay
  can deny new connections. It cannot read or alter existing P2P sessions.
  Registration proofs prevent unauthenticated third-party clients from replacing
  an already protected live agent registration, but the relay can still deny
  service by policy or outage.
- **TURN fallback (opt-in).** For symmetric NATs that cannot hole-punch, an
  operator may enable a TURN relay. Even then the relay forwards only ciphertext —
  Noise keeps it blind — but it does carry (encrypted) bytes and learns more
  timing/volume. It is **off by default**.
- **Compromised endpoints / Keychain.** Out of scope — the same trust you already
  place in your own devices.

## Live deployment operations

The live browser deployment is `https://term.sourceful-labs.net`; signaling may
also be exposed on `https://relay.sourceful-labs.net` for CLIs and agents. Apply
these controls to every Cloudflare hostname that routes to `mir-signal`.

- **Rate-limit public rendezvous endpoints.** `/turn-credentials`, `/pair`,
  `/attach`, and `/agent/signal` are intentionally public and unauthenticated at
  the HTTP layer. Protect them in Cloudflare WAF rate limiting rules before broad
  use. Treat `/pair`, `/attach`, and `/agent/signal` as WebSocket endpoints: the
  edge can rate-limit the initial HTTP upgrade request, but it will not inspect
  frames after the socket is established.
- **Monitor TURN as a paid abuse surface.** `/turn-credentials` issues coturn REST
  credentials when `MIR_TURN_SECRET` and `--turn-url` are configured. The current
  credential TTL is 12 hours, so an exposed credential can consume relay bandwidth
  until expiry. Watch both Cloudflare request volume and coturn allocation logs;
  rotate the shared secret and close TURN firewall ports if abuse appears.
- **Serve the SPA with defensive headers.** If `mir-signal --webroot` serves the
  browser app, set CSP and security headers at Cloudflare or in the origin before
  relying on the browser flow for real machines. The CSP must still allow module
  scripts/assets from the same origin and `connect-src` to the live signaling
  hostnames.
- **Keep the WebAuthn RP ID boundary small and intentional.** The browser code
  uses `term.sourceful-labs.net` — the exact app host — as the RP ID, so the
  owner passkey is bound to that single origin. Sibling `*.sourceful-labs.net`
  subdomains (including `relay`) are therefore **outside** the owner-key trust
  boundary: a passkey scoped to `term.sourceful-labs.net` cannot be exercised by
  another subdomain, and the registrable parent domain is **not** the RP ID.
  Keep it that way: do not widen the RP ID to the parent domain to "share"
  passkeys across subdomains without a deliberate re-enrollment plan, since that
  would pull every such subdomain into the trust boundary.
- **Require pairing safety-number confirmation.** A scanned/pasted pairing code is
  not enough for high-assurance pairing. The operator and browser/CLI user must
  compare the printed `safety number: xxxx-xxxx-xxxx-xxxx` out of band and abort
  on any mismatch.

## How to verify (don't take our word for it)

- **Read the code.** The crypto is small, standard, and isolated:
  `go/internal/noise` (Noise KK), `go/internal/pairing` (NNpsk0), the `prf`→owner
  derivation. Cross-language interop is pinned by `testdata/` vectors.
- **Run the tests.** `cd go && go test ./...` (and `-race`). The relay's
  blindness is structural: `go/internal/signal` only ever marshals SDP/`roomID`,
  never terminal bytes.
- **Watch the wire.** The data plane is a direct DataChannel; the relay never
  receives it. `deploy/netsim` reproduces real NAT scenarios locally.
- **Compare the safety number at pairing.** Both ends of `mir pair` each print a
  `safety number: xxxx-xxxx-xxxx-xxxx`. If both ends show the same value,
  you have visibly confirmed there is no MITM — even if the pairing token leaked.

## Roadmap to full, independent trust

These are the steps that let _anyone_ — not just us — trust the relay-free model:

- [ ] **Open source** the client, agent, relay, and crypto (so it is auditable).
- [ ] **Signed, checksummed releases** (and an installer that verifies them — never
      trust binaries from the relay).
- [ ] **Reproducible builds** (the binary you run matches the audited source).
- [x] **Verifiable pairing authenticity (safety number).** Both ends of
      `mir pair` print a 64-bit **safety number** derived from the Noise
      handshake transcript hash. With no MITM both ends show the same number;
      a man-in-the-middle (e.g. with a leaked token) produces two different
      handshakes → mismatched numbers, which you catch by eye. (Session-time SAS
      on `attach` is a planned extension.)
- [ ] **Independent third-party security audit** of the crypto and protocol.
- [ ] **Metadata minimization** (e.g. rotating/blinded `owner_id`s) where feasible.

## Reporting

Found a problem? Please report privately to security@sourceful-labs.net before
public disclosure.

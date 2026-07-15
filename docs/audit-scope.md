# Miranda v0.7 audit scope

This document is the hand-off boundary for an independent security review. The
claim to test is narrow: with uncompromised endpoints and confirmed pairing, the
relay cannot read or forge authenticated terminal plaintext.

## Highest-priority surfaces

1. Cross-language identity and crypto under `go/internal/{identity,noise,pairing}`
   and `web/src/{identity,noise,pairing}`; compare every domain separator,
   canonical encoding, transcript, nonce, and test vector.
2. Pairing/provisioning: bearer-token entropy and lifetime, NNpsk0 transcript
   authentication, 96-bit safety number, confirmation-before-persist, agent
   registration commitment.
3. Attach authorization in `go/internal/{agent,client,signal}` and `web/src/app.js`:
   session/SDP binding, replay behavior, allocation order, replacement races, LAN
   parity, and Noise KK peer pinning.
4. Machine revocation in Go and JavaScript: signature canonicalization, local-first
   persistence, relay durability, suppression behavior, pending-vs-established
   session semantics, and post-restart enforcement.
5. Secret handling and supply chain: OS-keychain invocation, migration/crash
   consistency, recovery import/export, agent/client state separation, cosign
   identity pinning, installer/self-update, deterministic builds, SPA/service-worker
   trust.
6. Relay abuse: all capacity bounds, fixed-window limits, trusted-proxy parsing,
   WebSocket deadlines, TURN issuance, persistent-store exhaustion, malformed
   bodies, and concurrent shutdown/re-registration.

## Explicit non-claims

- A compromised target Unix account, browser origin, client binary, passkey account,
  or unlocked owner keychain is outside the confidentiality guarantee.
- The relay can observe metadata, suppress revocations/discovery, and deny service.
- Miranda is not a privacy network, VPN, SSH implementation, multi-user access
  system, or sandbox for commands on the target.
- tmux, WebRTC/Pion, browser WebAuthn implementations, OS keychains, coturn, and the
  operating systems are dependencies, not reimplemented security boundaries.

## Evidence expected before sign-off

- `go test ./...`, full race suite, `npm test`, fuzz targets, and reproducibility
  check pass at the reviewed commit.
- No owner root/private key exists in agent state, relay state, logs, argv, or
  `owner.json`; the relay has no terminal-frame parser or plaintext data path.
- Findings include exploitability, affected invariant, reproduction, recommended
  fix, and a regression test. Retest every high/critical fix before release.

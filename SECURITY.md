# Miranda security model

Miranda exposes a real terminal. Its security boundary must therefore be smaller
and clearer than its marketing.

The core guarantee is:

> With uncompromised endpoints and correctly confirmed pairing, the rendezvous
> relay cannot read or forge authenticated terminal plaintext, even when it carries
> signaling or optional TURN ciphertext.

The relay can observe metadata and deny service. The target machine, client code,
owner identity, and initial pairing confirmation remain trusted components.

Miranda is an alpha and has not had an independent security audit.

## Security invariants

- The relay never receives terminal plaintext.
- The agent never receives or persists an owner root/private key.
- Browser owner identity is derived from WebAuthn PRF output for the exact app RP
  ID. Derived key material lives in page memory for the active ceremony/session.
- Go and JavaScript cryptography must remain byte-identical. Stable vectors under
  [`testdata/`](testdata/) gate identity, binding, registry, Noise, and pairing
  behavior.
- A target shell runs with the agent process's OS privileges. `mir up` refuses root
  by default.

## Components and trust

### Owner client

The owner client is the authority that can pair machines and authorize attaches.

- In the browser, a passkey's WebAuthn PRF output is the root. The RP ID is the
  exact hostname serving Miranda, not its parent domain.
- In the native CLI, the recovery root is stored in macOS Keychain or the Linux
  Secret Service. `~/.miranda/client/owner.json` (mode `0600`) contains only an
  opaque keychain reference and public metadata. Existing plaintext-root files
  migrate atomically on first load; there is no plaintext fallback.
- Domain-separated derivations produce the Ed25519 owner signer, X25519 Noise
  static key, and registry AEAD key.

Compromise of the passkey account, unlocked OS keychain/user account, browser
process, or client binary is compromise of the owner identity.

### Target agent

The target stores only machine-scoped authority in
`~/.miranda/agent/config.json`:

- its X25519 host private key;
- a random machine ID;
- a random relay-registration secret;
- pinned owner public IDs;
- owner signatures authorizing registration;
- opaque owner-encrypted discovery records.

It cannot decrypt or forge those discovery records and cannot authorize another
machine. Client and agent state directories are separate; `mir up` refuses a state
directory containing `owner.json`.

A compromised target still exposes everything available to the Unix account running
the agent. Use a dedicated unprivileged account, no passwordless sudo, and minimal
filesystem credentials. The `--allow-root` override is for isolated development,
not normal deployment.

### Rendezvous relay

The relay matches owner/machine IDs, forwards one-shot SDP messages, bridges pairing
rooms, serves temporary TURN credentials when configured, retains encrypted
registry blobs only with live agent registrations, and persists owner-signed
machine revocation tombstones.

It is not trusted for confidentiality, integrity, identity, or availability. It can:

- see `owner_id`, `machine_id`, source addresses, connection times, SDP/ICE data,
  candidate IP addresses, and traffic size/timing;
- correlate machines belonging to the same stable owner ID;
- drop, delay, replay, or alter control-plane messages;
- claim a machine is offline, withhold registry records, or deny all new sessions;
- observe an agent registration secret and its public owner authorization.
- suppress a real revocation record or lie about revocation availability.

It cannot use those capabilities to complete the pinned Noise handshake as the
owner or host. Tampering becomes a failed connection, not authenticated plaintext.
A malicious relay can always cause denial of service.

### Browser application origin

The JavaScript served by the Miranda web origin is a trust root equivalent to an
installed client binary. Compromise of the origin, Cloudflare account, deploy key,
service worker, or build output can read data before encryption or after decryption.

Self-hosting the relay does not remove this trust unless the browser app is also
self-hosted from an origin you control. Custom deployments use their exact hostname
as the WebAuthn RP ID and require a separate passkey enrollment.

## Protocol flows

### 1. Pairing and provisioning

1. The agent generates a 128-bit random token and displays it only in the QR/code.
2. The relay sees a domain-separated hash of that token as the room ID, not the
   token itself.
3. Both endpoints run `Noise_NNpsk0_25519_ChaChaPoly_SHA256`, with a PSK derived
   from the token.
4. The client claims an owner public ID, then signs the completed Noise transcript.
   The agent pins the owner only after this verifies.
5. Both endpoints display a 96-bit, six-group safety number derived from the
   transcript. Neither CLI endpoint persists trust until the human confirms it.
6. Inside the authenticated pairing channel, the owner sends:
   - an encrypted discovery record; and
   - a signature over the machine ID plus the SHA-256 commitment of the agent's
     registration secret.

The token is a temporary bearer secret. Scan it in person or send it over a trusted
channel, compare every safety-number group, and cancel on any mismatch. Possession
of a live token enables an active pairing attempt; the safety-number comparison is
what detects a substituted endpoint.

### 2. Agent registration

For every paired owner, the agent registers its `owner_id` + `machine_id` slot with:

- the 256-bit random local registration secret; and
- the owner's Ed25519 signature over that machine ID and the secret commitment.

The relay validates the owner signature before accepting the WebSocket. It then
uses a constant-time comparison against the first learned secret for replacement
defense. This closes first-registration squatting for real Miranda owner IDs while
keeping the owner key out of the agent.

The relay learns the registration credential over TLS. This credential controls
only relay availability for one slot; it is not a terminal or Noise credential.
Relay restarts lose the learned-secret cache, but the owner signature remains
independently verifiable.

### 3. Attach authorization

Before an agent allocates WebRTC, ICE, TURN, or Pion resources:

1. the relay creates a fresh 128-bit session ID and sends it to the client;
2. the client signs a domain-separated challenge containing that session ID,
   machine ID, and SHA-256 hash of the exact SDP offer;
3. the offer also carries the owner's signed binding to its X25519 Noise key;
4. the agent verifies that the owner is pinned, verifies both signatures, and
   rejects session replays.

Idle attach sockets have a short first-offer deadline. The relay and agent bound
message size, pair rooms, per-agent browser sessions, proof storage, and concurrent
pre-auth attaches. These are resource controls, not a substitute for edge rate
limits.

### 4. Terminal data plane

After authorization, the peers establish a WebRTC DataChannel and run
`Noise_KK_25519_ChaChaPoly_SHA256` inside it. Both static keys were authenticated
through pairing/binding, so transport DTLS certificates are not the identity
boundary.

Noise provides mutual authentication, forward secrecy for session traffic, and
authenticated encryption. A relay or network attacker may corrupt or suppress
ciphertext, causing disconnect, but cannot turn modified bytes into accepted
terminal plaintext.

### 5. Encrypted discovery

The passkey/owner client creates each machine record during pairing and seals it
with ChaCha20-Poly1305 under a domain-separated registry key. `machine_id` is AEAD
associated data. The target stores and republishes the opaque base64 record but
cannot open or alter it.

The relay holds records only in memory while the corresponding agent is live.
Clients discard records that fail AEAD authentication. The relay still sees the
stable owner/machine routing relationship.

### 6. Machine revocation

`mir machine revoke <name> --yes` and the browser revoke action create a permanent,
domain-separated Ed25519 tombstone over `owner_id`, `machine_id`, and timestamp.
The client stores it locally before publication. Honest relays verify the owner
signature, persist the record atomically, close the target's signaling registration
and pending attaches, and reject future registration/attach attempts for that slot.
An already-established P2P Noise session no longer traverses the relay and
cannot be remotely killed by it; the client performing revocation closes its own
session, while other already-connected clients must disconnect or sync the record.

Every client independently verifies fetched tombstones before filtering discovery
and attach. A relay cannot forge a revocation, but it can suppress one. Previously
cached local records remain enforced while offline or during relay failure. A fully
compromised owner root can authorize attaches and cannot be repaired by per-machine
revocation; rotate the entire identity and re-pair instead.

## Network paths

- **Direct (preferred):** ICE pairs host candidates on a shared LAN and
  server-reflexive candidates across NATs; STUN exposes candidate addresses as
  required for NAT traversal. Private LAN addresses appear in the SDP the relay
  brokers.
- **TURN (fallback):** available only when the deployment configures it. TURN
  forwards Noise ciphertext and learns traffic metadata/volume.

(`mir up --no-lan` and `mir attach --relay-only` are deprecated no-ops from
v0.8.0-beta.3: both modes ride the same connection.)

Miranda does not hide IP addresses or provide traffic anonymity. Use a separate
privacy network if that is a requirement.

## Recovery and rotation

- `mir identity export-recovery --yes` intentionally prints a 24-word recovery
  phrase. Anyone who sees it controls the owner identity.
- Import never accepts a phrase in process arguments. Interactive input is hidden;
  controlled automation must opt into `--stdin`.
- `mir identity rotate --yes` creates a new owner identity and requires pairing
  every target again.
- `mir machine revoke <name> --yes` permanently blocks a lost/compromised target
  for the current owner identity. The browser exposes the same action from the
  terminal toolbar.

## Supply chain

- CI actions are pinned to commit SHAs.
- Releases sign `checksums.txt` through GitHub Actions OIDC and cosign keyless
  signing.
- The installer and self-updater require cosign verification and fail closed if
  cosign or signature assets are missing.
- The browser vendors cryptographic dependencies locally and does not load them
  from a runtime CDN.
- Release binaries use `-trimpath`, omit VCS/build IDs, use the commit timestamp,
  and pass a two-clean-cache byte comparison in CI. Independent verification is
  documented in [`docs/release.md`](docs/release.md). Reproducibility and release
  provenance do not replace a source/security audit.

## Operational requirements

Before exposing a public deployment:

- keep the bounded in-process per-IP limits enabled and add edge/global limits to
  `/pair`, `/attach`, `/agent/signal`, `/registry`, `/revocations`, and
  `/turn-credentials`;
- monitor agent replacement/flap events, capacity events, TURN allocations, and
  unusual owner/machine cardinality;
- keep TURN credential TTL and bandwidth limits conservative;
- serve the SPA with the emitted CSP nonce and defensive security headers;
- protect the web origin and release workflow as production trust roots;
- require Full (strict) TLS to the origin, restrict the origin firewall to trusted
  proxy ranges, and back up the signed revocation file;
- run the agent under a dedicated non-root service account;
- test direct, symmetric-NAT, cellular, reconnect, and relay-outage paths.

## Known gaps before production

- independent third-party protocol and implementation audit;
- privacy improvements for stable owner/machine metadata;
- measured, abuse-tested limits on the actual public relay and TURN service;
- sustained fuzz campaigns beyond the CI smoke targets, plus hostile-network and
  real NAT/cellular interoperability testing;
- recovery testing across the supported macOS Keychain/Linux Secret Service
  environments and passkey browser matrix.

## Verification

Run both language suites after any cryptographic or protocol change:

```bash
cd go && go test ./...
cd ../web && npm test
cd .. && ./scripts/verify-reproducible.sh
```

If a legitimate handshake/derivation change updates a stable vector:

```bash
cd go
UPDATE_VECTORS=1 go test ./internal/noise/ -run TestInteropVectorsStable
go test ./...
cd ../web && npm test
```

The relay's lack of terminal plaintext is also structural: terminal frames exist in
the peer/Noise data plane, not in `go/internal/signal`.

## Reporting

Report vulnerabilities privately to **security@sourceful-labs.net** before public
disclosure. Include affected version/commit, reproduction steps, and expected impact.

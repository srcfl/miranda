# B2 — Wallet-signed device registry (stateless, encrypted, blind relay)

**Status:** Approved design (2026-06-14). Implements **B2** of the north-star
(`2026-06-10-north-star-mesh-wallet-identity-design.md`). Builds on B1 (wallet identity) +
B1.4 (wallet-signed bindings). The payoff of wallet-rooted identity: *your machines appear
everywhere, by name, with no manual `add-machine` and no SAS between your own devices.*

**Decisions (review, 2026-06-14):**
1. **Encrypted, relay fully blind.** Device records are AEAD-encrypted under a wallet-derived
   key; the relay only ever holds/serves opaque blobs.
2. **Relay stays STATELESS.** No persistent store, no database. The registry is **in-memory
   soft-state** that rides along with the existing live agent registrations and is rebuilt by
   the devices themselves on reconnect — exactly like today's live registrations.
3. **Zero-touch enrollment + notify.** A device that has the wallet self-publishes and works
   immediately; other devices print a one-line "new device joined" notice on first sight.

**Goal:** add a device = have the wallet (passkey-sync or `mir wallet import-phrase`) + start
serving → it appears, by name, on all your devices, attachable with no pairing. Discovery
only; the Noise data plane and B1.4 acceptance are unchanged.

---

## Where we are / the friction this kills

- B1 gave every device a wallet; B1.4 lets an agent accept *any* X25519 transport key the
  wallet signs a binding for. But discovery is still manual: `mir add-machine` / `mir pair`
  per machine, and **re-pairing every machine after `mir keygen --wallet`** (the friction
  Fredrik just hit).
- The relay already keeps **in-memory soft-state**: live agent registrations, pairing rooms,
  browser sessions — all ephemeral, all rebuilt when participants reconnect. The registry is
  the same shape: it does **not** need to be a database.

---

## The design

### Registry = soft-state on the live agent registration
When `mir up` connects to `/agent/signal?owner_id=<wallet>&machine_id=…` it sends, as the
first message, its **encrypted device record**. The relay holds that blob **in-memory, tied
to the live registration** (a short grace period absorbs a reconnect blink), and drops it
when the agent disconnects. A relay restart loses everything; agents reconnect (existing
backoff) and re-publish within seconds. **No disk, no DB, no write-auth endpoint.**

```
GET /registry?wallet=<base58>  ->  [ { machine_id, blob(base64) }, … ]   # live regs under W
```
The relay returns the opaque blobs of every currently-registered agent under that wallet. It
verifies nothing and decrypts nothing — a dumb, blind pass-through.

### The record: AEAD = encryption AND authenticity, for free
```
K_reg   = HKDF-SHA256(ikm = wallet_secret, salt = "miranda/registry/v1", info = "aead-key")
record  = { v:1, name, host_pub, signal_url, ts }            # JSON plaintext
blob    = ChaCha20-Poly1305_seal(K_reg, nonce(12B random), record, aad = machine_id)
          # wire blob = nonce || ciphertext||tag
```
ChaCha20-Poly1305 (IETF, 12-byte nonce) is the *same* AEAD Noise already uses, so it's
already vendored (`@noble/ciphers/chacha`) and in Go (`golang.org/x/crypto/chacha20poly1305`)
— no new primitive to bundle. Each device seals only a handful of records over its life (one
per reconnect), far below the 96-bit-nonce birthday bound, so a random nonce per seal is
safe. (B2.0 verifies the vendored bundle exports it and re-bundles if not, as we did for
pbkdf2.)
- Only a wallet-holder (has `wallet_secret` → `K_reg`) can produce a blob that **opens**. A
  forged/garbage blob from someone without the wallet fails the AEAD → the fetcher **silently
  drops it**. So the registry is self-authenticating with **no signature and no relay
  verification** — the relay stays blind *and* stateless.
- `machine_id` is the AEAD **associated data**, binding each blob to its slot so the relay
  can't move a blob between machine_ids.
- `machine_id` is the only plaintext (it's the GET slot key, and the relay already sees it at
  attach). `name` / `host_pub` / `signal_url` live inside the ciphertext.

### Agent — `mir up` auto-serves its own wallet + publishes
A machine that holds the wallet *is* one of your devices, so it needs no pairing to serve
you. On `mir up` with a wallet-rooted identity:
- **auto-pin its own wallet** as a served owner (`PinOwner(self_wallet)`) — so your clients
  (same wallet, B1.4 bindings) are accepted with no SAS;
- **publish** its encrypted record on the registration.

So a brand-new machine: `mir wallet import-phrase` → `mir up` → both accepts your clients
*and* appears in your list. Zero pairing.

### Client — discover, merge, notify
`mir list` / `mir attach` fetch `GET /registry?wallet=self`, AEAD-open each blob (drop
failures), and merge the results with the local `machines.json`. A discovered machine's
`host_pub` arrives wallet-authenticated (only your wallet could seal it) → Noise-KK pins it
directly, **no TOFU/SAS**. `mir attach <name>` resolves the name from the registry.

**Notify:** each device keeps a local "seen machine_ids" set; a machine_id newly present in
the registry prints `📣 new device "<name>" joined your wallet` once. Local, no relay role.

### Browser — auto-list
The browser already derives the wallet at sign-in (B1.5) → it has `wallet_secret` → `K_reg`.
It fetches the registry, decrypts, and **auto-populates "your machines" by name**, with the
same new-device notice. (The browser doesn't serve, so it only consumes the registry.)

### Trade-off (accepted, the cost of statelessness)
Discovery shows **online** (or recently-online, within the grace period) machines. A
powered-off machine isn't listed until it reconnects — but you can't attach to an offline
machine anyway, so the loss is cosmetic (no "last seen, offline" row).

### Revocation — honest in a stateless model
There's no persistent record to tombstone. "Revoke" = **turn the device off** (it stops
registering → disappears). For a real compromise (a leaked phrase), the only true recovery is
**rotating the wallet** (`mir keygen --wallet`). An explicit `mir device revoke` would be
theatre — an attacker with the phrase re-publishes anyway. The **notify** gives awareness;
rotation is the fix.

---

## What stays unchanged (guardrails)
- **Relay stays blind AND stateless.** It carries one opaque blob per live registration and
  serves a list; it never decrypts, verifies, or persists. No new durable state.
- **Acceptance is B1.4.** The agent still accepts any wallet-signed binding; the registry is
  **discovery only**, so **LAN-direct keeps working fully offline** (no registry fetch on the
  attach path).
- **`mir pair` / `mir add-machine` are kept** for cross-wallet (Track D seam) and manual
  override; they're just no longer needed for your own devices.
- **Noise-KK transport + existing `testdata/` vectors** untouched.

## Crypto discipline
- New `testdata/` vectors gate the registry crypto byte-identically Go↔JS: `K_reg` derivation
  and a ChaCha20-Poly1305 seal/open over a fixed `(K_reg, nonce, record, machine_id)`. Go
  (`golang.org/x/crypto/chacha20poly1305`) and JS (`@noble/ciphers/chacha`) must agree.
- `wallet_secret` is the existing 32-byte prf root; never stored beyond owner.json, never
  sent to the relay.

---

## Implementation order (TDD, small PRs)
- **B2.0** crypto: `identity.RegistryKey(secret)` + `SealRecord`/`OpenRecord` (XChaCha20-
  Poly1305, machine_id AAD), Go + JS, `testdata/registry-*.json` vectors.
- **B2.1** relay (`mir-signal`): accept the blob as the agent's first registration message;
  hold it in-memory on the `agentConn` (grace period on disconnect); `GET /registry?wallet=`
  returns live blobs. Blind + stateless. Forward-through/listing tests.
- **B2.2** agent: on `mir up` with a wallet, auto-pin own wallet + build + publish the record;
  republish on name/host change.
- **B2.3** client: registry fetch+open+merge in `mir list` / `mir attach`; `📣` notify on new
  machine_id; resolve `attach <name>` via the registry.
- **B2.4** browser: fetch+decrypt+auto-list your machines by name + notify.
- **B2.5** e2e + deploy. **Deploy of the new `mir-signal` (registry endpoint) is live-infra —
  Fredrik's hand, health-gated.** It is additive/backward-compatible (old clients ignore the
  endpoint).

---

## Non-goals (now)
- **Persistent registry / offline-machine listing.** Deliberately out — keeps the relay
  stateless. (A "last seen" view would need durable state.)
- **Per-client-device revocation / tombstones.** Compromise recovery is wallet rotation.
- **Cross-wallet sharing** (Track D — the kept `mir pair`/SAS is the seam).
- **DHT-hosted registry** (Track C4 — the relay hosts it for now; the GET/blob shape is
  DHT-portable later).

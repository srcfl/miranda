# B1 — Wallet Identity (passkey-prf → Solana HD wallet)

**Status:** Draft for review (2026-06-11). Implements **Track B1** of
`2026-06-10-north-star-mesh-wallet-identity-design.md`. Resolves the two forks that
umbrella spec flagged for B1: the **Noise-KK key topology** and the **owner_id
migration**.

**Goal:** add a Solana-compatible (Ed25519) wallet as the owner identity, derived from
the same passkey `prf` root, **without** changing the X25519 transport or churning the
Noise interop vectors. Crypto-enabled, not crypto-dependent; additive and reversible.

---

## Resolved forks

### Fork 1 — Key topology → **transport X25519 stays; wallet sits on top, signs a binding**
The Noise-KK data plane is **unchanged**. The owner's transport key stays the existing
X25519 (`go/internal/identity/owner.go`: `HKDF-SHA256(prf, "terminal-relay/owner/v1",
"x25519")`, byte-identical — do **not** touch it). The **wallet** (Ed25519) is a second,
independent key from the same `prf` root via a separate KDF domain. The wallet **signs a
binding** authorizing the device's X25519 transport key. Minimal churn: existing Noise
vectors and the KK handshake do not change; the wallet is a new identity *layer* on top.

> Rejected: moving every device to per-device X25519 uniformly. More symmetric but a
> larger pinning change for no near-term win; revisit in B2/registry if needed.

### Fork 2 — owner_id migration → **derive both; accept both during a window; new = base58**
- `owner_id` (relay routing key + agent pinned-owner key + registration-proof key) gains
  a **base58 Ed25519 (Solana address)** form. The legacy **X25519-hex** form keeps
  working.
- Relay + agent accept **either** form on the wire during the migration window (a slot is
  keyed by the *normalized* owner_id; hex and base58 for the same identity are distinct
  keys — they do not collide, and a device re-pairs once to move to base58).
- The Noise-KK static key pinned for the data plane stays the **X25519 transport** in both
  forms. The wallet→X25519 binding (below) is what lets a wallet-addressed peer prove its
  transport key.
- New pairings/registrations use base58. No forced migration; re-pair to upgrade.

---

## Derivation (the precise, byte-identical chain)

One 32-byte `prf` root, two domain-separated keys:

```
prf (32 bytes, from WebAuthn PRF; CLI: a generated 32-byte secret)
 │
 ├─▶ TRANSPORT (unchanged, byte-identical to today)
 │     HKDF-SHA256(ikm=prf, salt="terminal-relay/owner/v1", info="x25519") → 32B → X25519
 │
 └─▶ WALLET (new, additive)
       entropy   = prf                                   (256-bit BIP39 entropy)
       mnemonic  = BIP39.entropyToMnemonic(entropy)      (24 words, English wordlist)
       seed      = BIP39.mnemonicToSeed(mnemonic, "")    (PBKDF2-HMAC-SHA512, 2048, salt "mnemonic")
       node      = SLIP-0010-ed25519(seed, m/44'/501'/0'/0')   (all indices hardened)
       wallet    = Ed25519 keypair from node.key (32B seed)
       address   = base58(wallet.pub)                    (the Solana address = new owner_id)
```

- **No Ed25519↔X25519 conversion.** The two keys share only the `prf` root.
- `m/44'/501'/0'/0'` is the Phantom-importable account-0 path; HD sub-accounts use
  `m/44'/501'/i'/0'`. SLIP-0010 ed25519 is hardened-only (master = `HMAC-SHA512("ed25519
  seed", seed)`; each step `HMAC-SHA512(chainCode, 0x00 || key || ser32(i|0x80000000))`).
- The 24-word phrase is a deterministic **rendering** of `prf`, not a second root.
  Restoring from it reconstructs `prf` → re-derives **both** keys without the passkey.

### Libraries (stay in the noble/scure family for byte-identical parity)
- **JS:** `@noble/curves/ed25519` (already vendored), `@noble/hashes` (vendored: sha2,
  hmac, hkdf), add `@scure/bip39` (+ English wordlist) and `@scure/base58`; SLIP-0010
  ed25519 = a ~30-line helper over `@noble/hashes/hmac` (the chain above).
- **Go:** `crypto/ed25519` (stdlib), `golang.org/x/crypto/pbkdf2`+`hkdf` (have hkdf);
  add a vendored/minimal BIP39 (wordlist + entropy↔mnemonic + PBKDF2 seed), the same
  ~30-line SLIP-0010 helper, and base58 (`github.com/mr-tron/base58` or ~40 lines).
  Prefer tiny in-repo implementations of BIP39/SLIP-0010/base58 over a heavy dep, to keep
  the binary small and the parity auditable.

---

## Binding (wallet → transport)

A self-cert the wallet signs once per device:

```
binding = { v:1, wallet:<base58>, device:<machine_id>, x25519:<hex pub>, ts:<unix> }
msg     = canonical-encode(binding)              // fixed field order, no whitespace
sig     = Ed25519.sign(wallet.priv, "miranda/binding/v1" || msg)
record  = { ...binding, sig:<base58> }
```

A node running `mir up` holds its own device record (self-signed, since same wallet). Any
device with the wallet pubkey verifies `sig` and then accepts `x25519` as the device's
Noise-KK static key. This is the seam B2 (registry) and D (cross-wallet) build on.

## Auth (sign-in-with-Solana style, off-chain)

Pairing/attach carries a **wallet signature over a fresh server challenge**:
`sig = Ed25519.sign(wallet.priv, "miranda/auth/v1" || nonce)`. The relay still sees only
ciphertext + wallet-addressed routing metadata (base58). **Blind-relay invariant intact.**

---

## `mir wallet` commands

```
mir wallet address          print the base58 Solana address (the owner_id)
mir wallet accounts         list HD sub-accounts (m/44'/501'/i'/0')
mir wallet export-phrase    reveal the 24-word phrase (reveal-once, explicit warning)
mir wallet import-phrase    restore prf from a phrase (re-derives everything, no passkey)
```

`export-phrase`/`import-phrase` are the opt-in backup path. Default: phrase never shown.

---

## What stays unchanged (guardrails)

- **Noise KK data plane, X25519 transport derivation, existing `testdata/` Noise vectors**
  — untouched. CI gate `cd go && go test ./...` + `cd web && npm test` must stay green
  with the *current* vectors before any new vector is added.
- The passkey-prf invariant: private keys never stored; re-derived per device (or from the
  phrase). The CLI identity also becomes a BIP39 wallet (generated, exportable).

## New `testdata/` vectors (the gate, extended)

Add byte-identical Go↔JS vectors for: `prf → mnemonic`, `mnemonic → seed`, `seed →
m/44'/501'/0'/0' → ed25519 pub → base58 address`, the binding `msg`+`sig`, and an auth
`sig`. `UPDATE_VECTORS=1` regenerates. A known-answer cross-check against an independent
Solana lib (e.g. a Phantom-derived address for a fixed test phrase) pins external
correctness.

## Implementation order (TDD per task; small PRs)

1. **B1.0** in-repo `base58` + `slip10-ed25519` + `bip39` helpers, Go **and** JS, each
   with unit tests against published BIP39/SLIP-0010 test vectors. (No Miranda wiring yet.)
2. **B1.1** `DeriveWallet(prf)` Go+JS → address; shared `testdata/wallet-derivation.json`
   vector. Assert the existing X25519 derivation is unchanged in the same test.
3. **B1.2** binding sign/verify Go+JS + vector.
4. **B1.3** `mir wallet …` commands (address/accounts/export/import).
5. **B1.4** wire base58 owner_id through relay routing + agent pinning + registration
   proof, accepting **both** forms (Fork 2). Auth signature on pair/attach.
6. **B1.5** browser: derive wallet from prf, show address, wallet-signed auth.

Each step is independently shippable and never regresses Core. B2 (wallet-signed device
registry, replacing add-machine between your own devices) is a separate spec after B1.4.

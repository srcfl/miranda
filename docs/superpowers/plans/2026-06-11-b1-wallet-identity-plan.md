# B1 — Wallet Identity Implementation Plan

> **For agentic workers:** Implements `docs/superpowers/specs/2026-06-11-b1-wallet-identity.md`.
> TDD, byte-identical Go↔JS, published vectors as the gate. Steps use `- [ ]` tracking.

**Goal:** add a Solana-compatible (Ed25519) wallet identity derived from the passkey `prf`,
without touching the X25519 transport or the existing Noise interop vectors.

**Architecture:** one `prf` root → two domain-separated keys. Transport X25519 stays
byte-identical. Wallet = BIP39(prf) → SLIP-0010-ed25519 `m/44'/501'/0'/0'` → Ed25519 →
base58 address. New crypto primitives live in small, single-purpose packages with no
Miranda wiring until B1.4/B1.5.

**Tech Stack:** Go stdlib (`crypto/ed25519`, `crypto/sha512`, `crypto/hmac`,
`crypto/pbkdf2`); JS vendored `@noble/curves/ed25519` + `@noble/hashes` (sha2, hmac,
pbkdf2, utils). In-repo base58 / SLIP-0010 / BIP39 in **both** languages (tiny, auditable).

---

## External anchors (the gate)

Generated once via an independent oracle (`bip-utils`, Python) for a fixed
`prf = 00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff`. Both Go and JS
MUST reproduce every value below. (Regenerate: `uv run --with bip-utils python` over the
script in `docs/superpowers/specs/` notes; not a build dependency.)

- **mnemonic** (24w): `abandon math mimic master filter design carbon crystal rookie group knife wrap absurd much snack melt grid rough chapter fever rubber humble room trophy`
- **seed** (PBKDF2-HMAC-SHA512, 2048, pass=""): `559da5e7655dd1fbe657c100870512afb2b654b0acfd32f2c549344407e555bc16c2e71219eefc24acc7ed2cfaeac8a1808d543a5de4890bb2d95a7bb58af5b7`
- **node priv** `m/44'/501'/0'/0'`: `fb0d9e4a24019fc5d35ba3d44561d1b03d111ff670347af59c27d57304dafcd5`
- **ed25519 pub**: `a3d4ab895f8bc2990f27e64b4ee2abcb9396dc132ead962a1ba6664fd938ec41`
- **address (base58)**: `C2XYPfExbj6azVqYLWeUphzsdKK2dQ53dm83Brd3THmS`
- **BIP39 zero-entropy** anchors: 16B → `abandon … about`; 32B → `abandon ×23 art`.
- **SLIP-0010 ed25519 official Test Vector 1** (seed `000102…0f`): m / m/0' / m/0'/1' /
  m/0'/1'/2' / m/0'/1'/2'/2' / m/0'/1'/2'/2'/1000000000' — chain+priv+pub per node.
- **base58 Bitcoin vectors**: `""→""`, `61→2g`, `626262→a3gV`,
  `73696d706c792061206c6f6e6720737472696e67→2cFupjhnEsSn59qHXstmK2ffpLv2`,
  `00000000000000000000→1111111111`.

---

## File structure

**Go** (siblings of existing `go/internal/*`):
- `go/internal/base58/base58.go` (+ `_test.go`) — Bitcoin/Solana alphabet encode/decode.
- `go/internal/slip10/slip10.go` (+ `_test.go`) — SLIP-0010 ed25519 (hardened-only).
- `go/internal/bip39/bip39.go`, `wordlist.go` (+ `_test.go`) — entropy↔mnemonic, seed.

**JS** (new `web/src/wallet/`):
- `web/src/wallet/base58.js` (+ `web/test/base58.test.js`)
- `web/src/wallet/slip10.js` (+ `web/test/slip10.test.js`)
- `web/src/wallet/bip39.js`, `web/src/wallet/wordlist.js` (+ `web/test/bip39.test.js`)

Each file has one responsibility; the wordlist (2048 words, sha256
`2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda`) is shared data,
byte-identical in both languages.

---

## B1.0 — primitives (this PR)

### Task 1: base58 (Go, then JS)
- [ ] Encode/decode with the Bitcoin alphabet `123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz`; leading 0x00 bytes → leading `1`.
- [ ] Test against the 5 Bitcoin vectors above + round-trip property (decode∘encode = id) + decode rejects non-alphabet chars.
- [ ] Run: `cd go && go test ./internal/base58/` → PASS; `cd web && node --test test/base58.test.js` → PASS.

### Task 2: SLIP-0010 ed25519 (Go, then JS)
- [ ] master = `HMAC-SHA512("ed25519 seed", seed)` → (key=left32, chain=right32). Child (hardened only): `HMAC-SHA512(chain, 0x00 || key || ser32(i | 0x80000000))`. `DerivePath("m/44'/501'/0'/0'")`.
- [ ] Test every node of SLIP-0010 Test Vector 1 (chain+priv); assert `ed25519.pub(key)` equals the vector's pub[1:].
- [ ] Run the two suites → PASS.

### Task 3: BIP39 (Go, then JS)
- [ ] `entropyToMnemonic`: checksum = first `len/32` bits of `sha256(entropy)`; 11-bit groups → wordlist indices. `mnemonicToSeed`: `PBKDF2-HMAC-SHA512(mnemonic_NFKD, "mnemonic"+pass, 2048, 64)`.
- [ ] Test: zero-entropy 16B/32B anchors; prf→mnemonic anchor; mnemonic→seed anchor.
- [ ] Run the two suites → PASS.

### Task 4: full-suite gate + commit
- [ ] `cd go && go test ./...` (all green incl. untouched Noise/owner vectors).
- [ ] `cd web && npm test` (all green).
- [ ] Commit on `b1-wallet-identity`; open PR. No Miranda wiring touched.

---

## Forward map (separate PRs, planned as reached)

- **B1.1** `DeriveWallet(prf)` in `go/internal/identity` + `web/src/identity` → address;
  shared `testdata/wallet-derivation.json` (Go writes, JS asserts, like
  `owner-derivation.json`). Same test re-asserts X25519 derivation is unchanged.
- **B1.2** binding sign/verify (`miranda/binding/v1`) Go+JS + vector.
- **B1.3** `mir wallet address|accounts|export-phrase|import-phrase`.
- **B1.4** base58 owner_id through relay routing + agent pinning + registration proof,
  accepting **both** hex and base58 (Fork 2); wallet auth sig on pair/attach. (Live-infra
  adjacent — checkpoint before deploy.)
- **B1.5** browser: derive wallet from prf, show address, wallet-signed auth; add
  `@noble/hashes/pbkdf2` to the import map.

Each step independently shippable; never regresses Core (Noise KK + X25519 transport +
existing vectors stay byte-identical).

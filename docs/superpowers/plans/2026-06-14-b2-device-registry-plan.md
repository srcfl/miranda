# B2 — Wallet device registry — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** your machines appear everywhere by name, attachable with no `add-machine` and no
SAS — via a stateless, encrypted, blind-relay device registry. Spec:
`docs/superpowers/specs/2026-06-14-b2-device-registry-design.md`.

**Architecture:** each `mir up` agent seals an encrypted record `{name, host_pub, signal_url,
ts}` under `K_reg = HKDF(wallet_secret, "miranda/registry/v1")` and attaches it to its live
`/agent/signal` registration. The relay holds it in-memory (no persistence) and serves
`GET /registry?wallet=W → [{machine_id, blob}]`. Clients/browser fetch, AEAD-open (forgeries
self-drop), and auto-list. Discovery only; B1.4 acceptance + LAN-direct unchanged.

**Tech stack:** Go (`internal/{identity,signal,agent,client,cli}`), JS (`web/src`),
`golang.org/x/crypto/chacha20poly1305`, `@noble/ciphers/chacha`, `testdata/` vectors.

**Cross-cutting facts:**
- `wallet_secret` = the 32-byte prf root in `owner.json` (`Identity.Secret()`); never sent to
  the relay. `K_reg` derives from it.
- AEAD = ChaCha20-Poly1305 (IETF, 12-byte random nonce), `aad = machine_id`. Wire blob =
  `nonce || ciphertext||tag`. Seal/open must be byte-identical Go↔JS (vector-gated).
- The agent→relay channel is `signal.SignalMsg` (B1.4 added the opaque `Binding` field; B2
  adds an opaque `Registry` field the same way).
- Relay stays **blind + stateless**: it stores one opaque blob per *live* `agentConn` and
  serves a list; it never decrypts/verifies/persists.

---

## Task 1 — B2.0: registry crypto (Go + JS + vectors)

**Files:** Create `go/internal/identity/registry.go` + `registry_test.go`;
`web/src/identity/registry.js` + `web/test/registry.test.js`; `testdata/registry-vector.json`.

- [ ] **Step 1 (Go test first).** `TestRegistryKeyAndSeal`: from a fixed 32-byte secret,
  `RegistryKey(secret)` is deterministic; `SealRecord(key, nonce, plaintext, machineID)` then
  `OpenRecord(key, blob, machineID)` round-trips; a wrong `machineID` (AAD) fails open; a
  flipped ciphertext byte fails open.
- [ ] **Step 2: implement `registry.go`.**
  ```go
  const registrySalt = "miranda/registry/v1"

  // RegistryKey derives the symmetric registry key from the wallet's prf secret.
  func RegistryKey(secret []byte) ([]byte, error) {
      r := hkdf.New(sha256.New, secret, []byte(registrySalt), []byte("aead-key"))
      k := make([]byte, chacha20poly1305.KeySize)
      _, err := io.ReadFull(r, k)
      return k, err
  }

  // SealRecord encrypts plaintext under key with machineID as AAD; returns nonce||ct.
  func SealRecord(key, nonce, plaintext []byte, machineID string) ([]byte, error) {
      aead, err := chacha20poly1305.New(key); if err != nil { return nil, err }
      if len(nonce) != aead.NonceSize() { return nil, fmt.Errorf("registry: bad nonce") }
      ct := aead.Seal(nil, nonce, plaintext, []byte(machineID))
      return append(append([]byte{}, nonce...), ct...), nil
  }

  // OpenRecord reverses SealRecord; returns an error (not plaintext) on any failure.
  func OpenRecord(key, blob []byte, machineID string) ([]byte, error) {
      aead, err := chacha20poly1305.New(key); if err != nil { return nil, err }
      n := aead.NonceSize()
      if len(blob) < n { return nil, fmt.Errorf("registry: short blob") }
      return aead.Open(nil, blob[:n], blob[n:], []byte(machineID))
  }
  ```
- [ ] **Step 3: run** `cd go && go test ./internal/identity/ -run Registry -v` (fail→pass).
- [ ] **Step 4: JS mirror + vendored-cipher check.** Confirm the vendored
  `web/vendor/noble-ciphers-chacha.js` exports `chacha20poly1305`; if not, re-bundle via
  `npx esbuild node_modules/@noble/ciphers/chacha.js --bundle --format=esm --minify` (as we
  did for pbkdf2) and update the importmap/sw.js. Create `web/src/identity/registry.js`:
  ```js
  import { chacha20poly1305 } from '@noble/ciphers/chacha';
  import { hkdf } from '@noble/hashes/hkdf';
  import { sha256 } from '@noble/hashes/sha2';
  const SALT = new TextEncoder().encode('miranda/registry/v1');
  const INFO = new TextEncoder().encode('aead-key');
  export function registryKey(secret) { return hkdf(sha256, secret, SALT, INFO, 32); }
  export function sealRecord(key, nonce, plaintext, machineID) {
    const aad = new TextEncoder().encode(machineID);
    const ct = chacha20poly1305(key, nonce, aad).encrypt(plaintext);
    const out = new Uint8Array(nonce.length + ct.length); out.set(nonce); out.set(ct, nonce.length);
    return out;
  }
  export function openRecord(key, blob, machineID) {
    const aad = new TextEncoder().encode(machineID);
    return chacha20poly1305(key, blob.slice(0, 12), aad).decrypt(blob.slice(12)); // throws on failure
  }
  ```
- [ ] **Step 5: vector.** `testdata/registry-vector.json`: fixed `secret`, derived `key`
  (hex), fixed `nonce` (hex), a fixed `record` JSON, `machine_id`, and `blob` (hex). Generate
  with `UPDATE_VECTORS=1 go test ./internal/identity/ -run RegistryVector`; assert the JS test
  reproduces `key` and `blob` and that `openRecord` round-trips. Cross-check the AEAD output
  once against an independent oracle (`uv run --with cryptography`) so the gate rests on more
  than Go↔JS self-agreement.
- [ ] **Step 6:** `cd go && go test ./...` and `cd web && npm test` green. Commit:
  `feat(identity): B2.0 registry key + ChaCha20-Poly1305 record seal/open (Go+JS+vector)`.

---

## Task 2 — B2.1: relay registry (blind, stateless)

**Files:** Modify `go/internal/signal/protocol.go` (add `Registry` field), `signal/server.go`
(store blob on agentConn + `GET /registry`), `go/cmd/mir-signal` route. Test:
`signal/server_test.go`.

- [ ] **Step 1: test first.** `TestRegistryListsLiveAgents`: register two fake agents under the
  same `owner_id=W` (different machine_ids), each sending a first `SignalMsg{Type:"registry",
  Registry: <blob>}`; `GET /registry?wallet=W` returns both `{machine_id, blob}` entries; a
  disconnected agent drops out; an agent under a different wallet is not listed.
- [ ] **Step 2: protocol.** Add `Registry string `json:"registry,omitempty"`` to `SignalMsg`
  and a `TypeRegistry = "registry"` const.
- [ ] **Step 3: store the blob.** In `handleAgent`'s read loop, on `Type == TypeRegistry`
  record `ac.registry = m.Registry` (add a `registry string` field to `agentConn`, guarded by
  its mutex). It rides the existing live connection — dropped automatically when `ac` is torn
  down. (Optional small grace: keep serving the last blob for `registryGrace` after
  disconnect; default 0 to start — reconnect re-publishes fast.)
- [ ] **Step 4: GET handler.** `handleRegistry(w, r)`: read `wallet=`; scan `s.agents` for keys
  with prefix `wallet+"|"`; for each live `ac` with a non-empty `ac.registry`, append
  `{"machine_id": <machine>, "blob": <base64>}`; write JSON. No auth, no decrypt (blobs are
  opaque + self-authenticating). Register `mux.HandleFunc("/registry", s.handleRegistry)`.
- [ ] **Step 5:** `cd go && go test ./internal/signal/`; `go vet`, `gofmt`. Commit:
  `feat(signal): B2.1 stateless blind device registry (blob on live reg + GET /registry)`.

---

## Task 3 — B2.2: agent publishes + auto-serves its own wallet

**Files:** Modify `go/internal/agent/runtime.go` (publish blob on connect), `go/internal/cli/
agent_cmds.go` (`cmdUp` loads the wallet + auto-pin), maybe `agent/store.go`. Test:
`agent/*_test.go`.

- [ ] **Step 1: cmdUp loads the wallet + auto-pins.** In `cmdUp`, after `agent.LoadOrInit`,
  load the client identity (`a.identity(*dir)`); if it `HasWallet()`, `agent.PinOwner(*dir,
  id.WalletAddress)` (idempotent) so the machine serves its own wallet with no pairing, and
  pass the wallet secret into the Runtime (e.g. `rt.SetWallet(secret)`), so it can build the
  registry record. A wallet-less (legacy) `mir up` keeps today's behavior (serve PairedOwners,
  no registry publish).
- [ ] **Step 2: build + publish the record.** Add to `Runtime` a method that, on each healthy
  registration for `owner == self_wallet`, builds `record = {v:1, name, host_pub, signal_url,
  ts}`, `blob = identity.SealRecord(RegistryKey(secret), rand12, json(record), machineID)`,
  and sends `SignalMsg{Type: signal.TypeRegistry, Registry: base64(blob)}` as the **first**
  message after the relay accepts the registration (in `serveOnce`, right after connect).
- [ ] **Step 3: test.** `TestAgentPublishesRegistryRecord`: a fake relay captures the first
  message on the agent connection; assert it's a `TypeRegistry` whose blob `OpenRecord`s (with
  the test wallet's `K_reg`) to a record carrying the machine's name + host_pub. Verify
  `cmdUp` auto-pins the wallet (`IsOwnerPinned(wallet)` true after).
- [ ] **Step 4:** `cd go && go test ./internal/agent/ ./internal/cli/`; vet/gofmt. Commit:
  `feat(agent): B2.2 mir up auto-serves own wallet + publishes encrypted registry record`.

---

## Task 4 — B2.3: client discovery (fetch, merge, notify)

**Files:** Create `go/internal/client/registry.go` (+ test); modify `cli/client_cmds.go`
(`cmdList`, `cmdAttach`), maybe `client/store.go` (seen-set for notify).

- [ ] **Step 1: registry fetch.** `client.FetchRegistry(ctx, signalURL string, id *Identity)
  ([]Machine, error)`: `GET <signalURL>/registry?wallet=<id.WalletAddress>`; for each entry,
  `OpenRecord(RegistryKey(id.Secret()), blob, machine_id)` (drop on error); JSON-parse the
  record → `Machine{Name, MachineID: machine_id, HostPubHex: rec.host_pub, SignalURL:
  rec.signal_url}`. Returns the discovered machines.
- [ ] **Step 2: merge + notify helper.** `mergeMachines(local, discovered)` (discovered fills
  in machines not in local; local wins on name conflicts). `notifyNewDevices(a.errOut, dir,
  discovered)`: load a `seen.json` set of machine_ids, print `📣 new device "<name>" joined
  your wallet` for each unseen, persist the updated set. Tests: merge precedence; notify fires
  once per new machine_id.
- [ ] **Step 3: wire `mir list`.** `cmdList` merges `ListMachines(dir)` with
  `FetchRegistry(...)` (best-effort — a relay error or no wallet falls back to local only) and
  prints the union, marking discovered ones. Notify on new.
- [ ] **Step 4: wire `mir attach <name>`.** Resolve the name from local machines first, then
  the registry; on a registry hit, attach using the wallet-authenticated `host_pub` (no
  add-machine needed). Best-effort registry; LAN-direct/relay attach paths unchanged.
- [ ] **Step 5:** `cd go && go test ./internal/client/ ./internal/cli/`; vet/gofmt. Commit:
  `feat(client): B2.3 registry discovery — list/attach auto-find your machines + notify`.

---

## Task 5 — B2.4: browser auto-list

**Files:** Create `web/src/identity/registry.js` (done in B2.0) usage in `web/src/app.js`
(+ `web/src/store.js`); `web/sw.js` (precache registry.js). Test: `web/test/*`.

- [ ] **Step 1:** add a `fetchRegistry(signalURL, wallet)` in `web/src/store.js` (or app.js):
  `fetch(signalURL + '/registry?wallet=' + wallet.address)`, `openRecord(registryKey(prf), …)`
  each blob (drop failures), return machines.
- [ ] **Step 2:** the machine-list view merges stored machines with the fetched registry, shows
  names, and surfaces a "new device joined" notice (mirror the CLI's seen-set in localStorage).
- [ ] **Step 3:** `web/sw.js` precache `/src/identity/registry.js` (sorted); the importmap test
  (added in the pbkdf2 fix) keeps `@noble/ciphers/chacha` honest.
- [ ] **Step 4:** `cd web && npm test`. Commit:
  `feat(web): B2.4 auto-list your machines from the encrypted registry + notify`.

---

## Task 6 — B2.5: e2e + deploy

- [ ] **Step 1: e2e (no manual add-machine).** In-process relay + a `mir up` (wallet) that
  publishes + a client that `FetchRegistry` discovers it and attaches by name — asserting a
  shell round-trips with **no `add-machine`/`pair`**. Negative: a forged blob (sealed under a
  different key) is silently dropped by the client.
- [ ] **Step 2: relay still blind + stateless.** A test asserting `GET /registry` returns only
  opaque blobs and that nothing is persisted across a fresh `Server` (in-memory only).
- [ ] **Step 3: gates.** `cd go && go test ./... && go vet ./... && gofmt -l .` clean;
  `cd web && npm test`; registry vector stable; Noise/wallet/binding vectors untouched.
- [ ] **Step 4: docs.** README ("your machines appear automatically") + SECURITY.md (registry
  is encrypted/blind/stateless; revocation = power off / rotate).
- [ ] **Step 5: deploy.** **Fredrik's hand:** redeploy `mir-signal` (adds `/registry` +
  the `Registry` passthrough — additive, backward-compatible). Health-gated.
- [ ] **Step 6: commit** `test(registry): B2.5 e2e discovery + blind/stateless relay guards`.

---

## Self-review notes
- **Spec coverage:** Tasks 1–6 cover B2.0 (crypto) → B2.5 (e2e+deploy). Relay stays
  blind+stateless (blob on live reg, no persistence, no verify). Discovery-only (LAN-direct +
  B1.4 acceptance untouched). `pair`/`add-machine` kept.
- **Type consistency:** `RegistryKey`/`SealRecord`/`OpenRecord`, `SignalMsg.Registry` +
  `TypeRegistry`, `agentConn.registry`, `FetchRegistry`, the `{v,name,host_pub,signal_url,ts}`
  record, and the `machine_id` AAD are referenced consistently across tasks and Go↔JS.
- **No new persistence:** the registry is in-memory soft-state on `agentConn`; a relay restart
  loses it and the agents rebuild it — verified in B2.5 Step 2.
- **Deploy is additive:** old clients ignore `/registry`; old agents simply publish nothing.

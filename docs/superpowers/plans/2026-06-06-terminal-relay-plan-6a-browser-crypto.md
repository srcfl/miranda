# terminal-relay — Plan 6a: browser pairing crypto (JS NNpsk0 + SAS), Go-interop certified

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** de-risk the browser's pairing crypto before building the SPA. Port the
Go pairing primitives to browser JS — `Noise_NNpsk0` handshake, the pairing-code /
`roomID` / `psk` derivation, and the safety number — and **certify byte-for-byte
interop with the Go reference** (same approach that made Plan 1's Noise KK
trustworthy). The browser already has Plan 1's `noise-kk.js` (attach handshake),
`frame.js`, and `owner.js` (`prf`→owner key); this plan adds the pairing pieces.

**Architecture:** Go (`flynn/noise`) is the reference. Fix the PSK + ephemerals,
emit the NNpsk0 wire bytes + the code/room/sas vectors to `testdata/`, and have
the JS reproduce them exactly. The JS NNpsk0 is hand-rolled on `@noble` (like
`noise-kk.js`); the vectors are the spec — iterate the JS until they match.

**Tech Stack:** Go `flynn/noise`; browser JS `@noble/*` (already a dep), Node `node --test`.

**Implementer notes:**
- NNpsk0 differs from KK: pattern is `psk, e` / `e, ee`, psk-placement 0. In PSK
  mode the `e` token also calls **MixKey** (not just MixHash) per the Noise spec.
  Get it right by matching the Go-generated vectors (Task 1) — do not guess; iterate.
- Constants must match Go exactly: protocol `Noise_NNpsk0_25519_ChaChaPoly_SHA256`,
  prologue `terminal-relay/pair/v1`; `psk = SHA256("terminal-relay/pair/psk"||token)`;
  `roomID = hex(SHA256("terminal-relay/pair/room"||token)[:16])`; SAS =
  `SHA256("terminal-relay/sas/v1"||channelBinding)` first 8 bytes as `xxxx-xxxx-xxxx-xxxx`.
- Run everything for real; the interop test (Task 4) is the gate — JS must
  reproduce the Go bytes. If a true incompatibility remains, STOP and report the
  first differing offset (expected vs got hex).

## File structure

```
go/internal/pairing/interop_test.go     # generate testdata vectors from flynn/noise (Go reference)
testdata/pair-interop.json              # NNpsk0 msg1/msg2 + code/room/psk/sas vectors (generated)
web/src/pairing/code.js   web/test/pairing-code.test.js   # EncodeCode/DecodeCode/roomID/psk
web/src/pairing/sas.js    web/test/pairing-sas.test.js    # safety number
web/src/pairing/nnpsk0.js web/test/pairing-interop.test.js # JS NNpsk0 reproduces the Go vectors
```

---

## Task 1: Generate Go pairing interop vectors

**Files:** Create `go/internal/pairing/interop_test.go`; generated `testdata/pair-interop.json`

- [ ] **Step 1: Write the generator/regression test**

```go
// go/internal/pairing/interop_test.go
package pairing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/flynn/noise"

	"github.com/srcful/terminal-relay/go/internal/sas"
)

var (
	fxToken   = mustHex("00112233445566778899aabbccddeeff")            // 16-byte token
	fxInitEph = mustHex("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	fxRespEph = mustHex("2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40")
	fxOwner   = mustHex("a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf") // owner pub
	fxInfo    = `{"host_pub":"5051525354555657585950515253545550515253545556575859505152535455","machine_id":"m42","name":"box"}`
)

func mustHex(s string) []byte { b, _ := hex.DecodeString(s); return b }

type fixedReader struct {
	data []byte
	pos  int
}

func (r *fixedReader) Read(p []byte) (int, error) { n := copy(p, r.data[r.pos:]); r.pos += n; return n, nil }

type pairVectors struct {
	Token      string `json:"token"`
	OwnerPub   string `json:"owner_pub"`
	InfoJSON   string `json:"info_json"`
	RoomID     string `json:"room_id"`
	PSK        string `json:"psk"`
	Msg1       string `json:"msg1"`
	Msg2       string `json:"msg2"`
	SafetyNum  string `json:"safety_number"`
}

func nnpsk0(initiator bool) *noise.HandshakeState {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	eph := fxInitEph
	if !initiator {
		eph = fxRespEph
	}
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite: cs, Pattern: noise.HandshakeNN, Initiator: initiator,
		Prologue: []byte("terminal-relay/pair/v1"),
		PresharedKey: pskFromToken(fxToken), PresharedKeyPlacement: 0,
		Random: &fixedReader{data: eph},
	})
	return hs
}

func runFixed(t *testing.T) pairVectors {
	t.Helper()
	ini := nnpsk0(true)
	res := nnpsk0(false)
	msg1, _, _, err := ini.WriteMessage(nil, fxOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := res.ReadMessage(nil, msg1); err != nil {
		t.Fatal(err)
	}
	msg2, _, _, err := res.WriteMessage(nil, []byte(fxInfo))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ini.ReadMessage(nil, msg2); err != nil {
		t.Fatal(err)
	}
	psk := sha256.Sum256(append([]byte("terminal-relay/pair/psk"), fxToken...))
	return pairVectors{
		Token: hex.EncodeToString(fxToken), OwnerPub: hex.EncodeToString(fxOwner),
		InfoJSON: fxInfo, RoomID: RoomID(fxToken), PSK: hex.EncodeToString(psk[:]),
		Msg1: hex.EncodeToString(msg1), Msg2: hex.EncodeToString(msg2),
		SafetyNum: sas.FromBinding(ini.ChannelBinding()),
	}
}

func TestPairInteropVectorsStable(t *testing.T) {
	v := runFixed(t)
	path := filepath.Join("..", "..", "..", "testdata", "pair-interop.json")
	if os.Getenv("UPDATE_VECTORS") == "1" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		data, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("pair vectors written")
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want pairVectors
	_ = json.Unmarshal(raw, &want)
	if v.Msg1 != want.Msg1 || v.Msg2 != want.Msg2 || v.SafetyNum != want.SafetyNum {
		t.Fatalf("Go pairing drifted from committed vectors")
	}
	_ = io.Discard
	_ = bytes.Equal
}
```

- [ ] **Step 2: Generate + commit**

```bash
cd go && UPDATE_VECTORS=1 go test ./internal/pairing/ -run TestPairInteropVectorsStable
go test ./internal/pairing/ -run TestPairInteropVectorsStable   # regression form passes
```
Confirm `testdata/pair-interop.json` exists. Commit:
```bash
git add go/internal/pairing/interop_test.go testdata/pair-interop.json
git commit -m "test(pairing): deterministic Go NNpsk0 + code/sas interop vectors"
```

---

## Task 2: JS pairing code + roomID + psk

**Files:** Create `web/src/pairing/code.js`, `web/test/pairing-code.test.js`

- [ ] **Step 1: Failing test (matches the Go vector)**

```js
// web/test/pairing-code.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { roomID, pskFromToken, encodeCode, decodeCode } from '../src/pairing/code.js';

const here = dirname(fileURLToPath(import.meta.url));
const v = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'pair-interop.json'), 'utf8'));

test('roomID and psk match the Go vector', () => {
  const tok = hexToBytes(v.token);
  assert.equal(roomID(tok), v.room_id);
  assert.equal(bytesToHex(pskFromToken(tok)), v.psk);
});

test('pairing code round-trips', () => {
  const code = encodeCode('https://relay.sourceful-labs.net', hexToBytes(v.token));
  const { signalURL, token } = decodeCode(code);
  assert.equal(signalURL, 'https://relay.sourceful-labs.net');
  assert.equal(bytesToHex(token), v.token);
});
```

- [ ] **Step 2: Run (fails: undefined), then write `code.js`**

```js
// web/src/pairing/code.js
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';

const enc = new TextEncoder();
function concat(a, b) { const o = new Uint8Array(a.length + b.length); o.set(a); o.set(b, a.length); return o; }

export function pskFromToken(token) {
  return sha256(concat(enc.encode('terminal-relay/pair/psk'), token));
}
export function roomID(token) {
  return bytesToHex(sha256(concat(enc.encode('terminal-relay/pair/room'), token)).slice(0, 16));
}
export function encodeCode(signalURL, token) {
  const json = JSON.stringify({ s: signalURL, t: bytesToHex(token) });
  return btoa(json).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); // base64url, no pad
}
export function decodeCode(code) {
  const b64 = code.replace(/-/g, '+').replace(/_/g, '/');
  const json = atob(b64);
  const p = JSON.parse(json);
  return { signalURL: p.s, token: hexToBytesLocal(p.t) };
}
function hexToBytesLocal(h) {
  const out = new Uint8Array(h.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  return out;
}
```
Run: `cd web && node --test test/pairing-code.test.js` → PASS. Commit.

---

## Task 3: JS safety number

**Files:** Create `web/src/pairing/sas.js`, `web/test/pairing-sas.test.js`

- [ ] **Step 1: Failing test (matches the Go vector's safety_number for the handshake binding)**

The binding itself comes from the NNpsk0 handshake (Task 4); here just verify the
SAS *formatting* function against a known input by reusing the Go formula on the
committed binding is covered in Task 4. For this task, unit-test the format:

```js
// web/test/pairing-sas.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { safetyNumber } from '../src/pairing/sas.js';

test('safety number is 4 groups of 4 hex, deterministic', () => {
  const a = safetyNumber(new Uint8Array([1, 2, 3, 4, 5]));
  assert.match(a, /^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$/);
  assert.equal(a, safetyNumber(new Uint8Array([1, 2, 3, 4, 5])));
});
```

- [ ] **Step 2: Write `sas.js`**

```js
// web/src/pairing/sas.js
import { sha256 } from '@noble/hashes/sha2';
import { bytesToHex } from '@noble/hashes/utils';

const enc = new TextEncoder();
export function safetyNumber(binding) {
  const h = sha256(concat(enc.encode('terminal-relay/sas/v1'), binding)).slice(0, 8);
  const hex = bytesToHex(h);
  return `${hex.slice(0, 4)}-${hex.slice(4, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}`;
}
function concat(a, b) { const o = new Uint8Array(a.length + b.length); o.set(a); o.set(b, a.length); return o; }
```
Run the test → PASS. Commit.

---

## Task 4: JS NNpsk0 handshake — reproduce the Go vectors (the gate)

**Files:** Create `web/src/pairing/nnpsk0.js`, `web/test/pairing-interop.test.js`

- [ ] **Step 1: Write the interop test (reproduces Go msg1/msg2 + SAS)**

```js
// web/test/pairing-interop.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils';
import { runInitiator, runResponder } from '../src/pairing/nnpsk0.js';
import { safetyNumber } from '../src/pairing/sas.js';

const here = dirname(fileURLToPath(import.meta.url));
const v = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'pair-interop.json'), 'utf8'));

// Deterministic in-memory pipe of discrete messages.
function pipe() {
  const a2b = [], b2a = [];
  const mk = (out, inn) => ({
    send: (m) => out.push(m),
    recv: async () => { for (;;) { if (inn.length) return inn.shift(); await new Promise((r) => setTimeout(r, 1)); } },
  });
  return [mk(a2b, b2a), mk(b2a, a2b)];
}

test('JS NNpsk0 reproduces the Go pairing wire bytes + safety number', async () => {
  const [clientMC, agentMC] = pipe();
  const token = hexToBytes(v.token);
  const ownerPub = hexToBytes(v.owner_pub);
  const info = JSON.parse(v.info_json);

  // fixed ephemerals so the bytes are deterministic (match the Go vectors)
  const agentP = runResponder(agentMC, token, info, hexToBytes('2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40'));
  const client = await runInitiator(clientMC, token, ownerPub, hexToBytes('0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20'));
  const agent = await agentP;

  // The wire bytes the client/agent put on the pipe must equal the Go vectors.
  // (Expose them via the conn or assert through a capturing pipe — see note.)
  assert.equal(client.info.host_pub, info.host_pub);
  assert.equal(bytesToHex(agent.ownerPub), v.owner_pub);
  assert.equal(safetyNumber(client.binding), v.safety_number);
  assert.equal(safetyNumber(agent.binding), v.safety_number);
});
```

> Implementer note: to assert the exact `msg1`/`msg2` bytes, capture them in the
> pipe (record what `send` receives) and compare to `v.msg1`/`v.msg2`. The
> safety-number equality already proves the channel bindings (hence the whole
> transcript) match Go. Make BOTH checks pass.

- [ ] **Step 2: Write `nnpsk0.js`**

Hand-roll `Noise_NNpsk0_25519_ChaChaPoly_SHA256` on `@noble` (mirror the structure
of `web/src/noise/noise-kk.js`, but pattern `psk,e` / `e,ee`, psk-placement 0, and
in PSK mode the `e` token calls **MixKey** as well as MixHash). `runInitiator`
sends `ownerPub` in msg1, reads the agent info from msg2, returns
`{ info, binding }` (binding = the symmetric-state hash `h` after the handshake).
`runResponder` reads `ownerPub` from msg1, sends `info` in msg2, returns
`{ ownerPub, binding }`. Accept an optional fixed ephemeral for tests.

Iterate against the Task-1 vectors until `msg1`, `msg2`, and the safety number
match byte-for-byte. (The vectors are the spec; this is the same de-risk loop as
Plan 1's Noise KK.)

- [ ] **Step 3: Run + verify whole JS suite**

```bash
cd web && node --test test/pairing-interop.test.js && npm test
```
Expected: PASS — JS NNpsk0 reproduces the Go bytes; all web tests green.

- [ ] **Step 4: Commit**

```bash
git add web/src/pairing/ web/test/pairing-*.test.js
git commit -m "feat(web): JS NNpsk0 pairing + SAS, certified against Go vectors"
```

---

## Self-review (during planning)

- **Spec coverage:** provides the browser's pairing crypto (NNpsk0 + code/room/psk
  + SAS), certified byte-for-byte against the Go reference — the same trust basis
  as Plan 1's Noise KK. Reuses Plan 1's `noise-kk.js`/`frame.js`/`owner.js`.
- **Gate:** Task 4 is the real de-risk — JS must reproduce the Go NNpsk0 wire bytes
  and safety number. SAS equality proves transcript equality.
- **Out of scope (Plan 6b, interactive):** the browser SPA — WebAuthn `prf`,
  `RTCPeerConnection` + the signaling client, xterm.js, the QR-fragment pairing UI,
  and serving on `term.sourceful-labs.net`. Those need a real browser to validate
  and are built interactively, not in this headless plan.
- **Consistency:** constants (protocol name, prologue, psk/room/sas domains) match
  Go exactly; that is what makes the vectors line up.

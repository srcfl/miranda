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

// Deterministic in-memory pipe of discrete messages. `sent` records every frame
// pushed onto each direction so the test can assert the exact wire bytes.
function pipe() {
  const a2b = [], b2a = [];
  const sent = { a2b: [], b2a: [] };
  const mk = (out, inn, log) => ({
    send: (m) => {
      sent[log].push(m);
      out.push(m);
    },
    recv: async () => {
      for (;;) {
        if (inn.length) return inn.shift();
        await new Promise((r) => setTimeout(r, 1));
      }
    },
  });
  // a = client (initiator), b = agent (responder)
  return [mk(a2b, b2a, 'a2b'), mk(b2a, a2b, 'b2a'), sent];
}

test('JS NNpsk0 reproduces the Go pairing wire bytes + safety number', async () => {
  const [clientMC, agentMC, sent] = pipe();
  const token = hexToBytes(v.token);
  const ownerPub = hexToBytes(v.owner_pub);
  const info = JSON.parse(v.info_json);

  // fixed ephemerals so the bytes are deterministic (match the Go vectors)
  const agentP = runResponder(agentMC, token, info, hexToBytes('2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40'));
  const client = await runInitiator(clientMC, token, ownerPub, hexToBytes('0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20'));
  const agent = await agentP;

  // The exact wire bytes the client/agent put on the pipe must equal the Go vectors.
  assert.equal(bytesToHex(sent.a2b[0]), v.msg1, 'msg1 (client->agent) must match Go');
  assert.equal(bytesToHex(sent.b2a[0]), v.msg2, 'msg2 (agent->client) must match Go');

  // Decrypted payloads + channel bindings (safety number) must match Go too.
  assert.equal(client.info.host_pub, info.host_pub);
  assert.equal(bytesToHex(agent.ownerPub), v.owner_pub);
  assert.equal(safetyNumber(client.binding), v.safety_number);
  assert.equal(safetyNumber(agent.binding), v.safety_number);
});

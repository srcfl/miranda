// web/test/rp.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resolveRPID } from '../src/rp.js';

test('production WebAuthn RP ID is the exact terminal app host', () => {
  assert.equal(resolveRPID('term.sourceful-labs.net'), 'term.sourceful-labs.net');
});

test('production RP ID is not widened to the parent domain', () => {
  assert.notEqual(resolveRPID('term.sourceful-labs.net'), 'sourceful-labs.net');
  assert.equal(resolveRPID('app.sourceful-labs.net'), 'app.sourceful-labs.net');
  assert.equal(resolveRPID('relay.sourceful-labs.net'), 'relay.sourceful-labs.net');
});

test('self-hosted domains get their own exact RP ID', () => {
  assert.equal(resolveRPID('terminal.example.com'), 'terminal.example.com');
  assert.throws(() => resolveRPID('terminal.example.com:443'));
});

test('localhost keeps its local development RP ID', () => {
  assert.equal(resolveRPID('localhost'), 'localhost');
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { iceSessionDead } from '../src/net/ice-state.js';

test('disconnected does not end an established session', () => {
  assert.equal(iceSessionDead('disconnected'), false);
  assert.equal(iceSessionDead('connected'), false);
  assert.equal(iceSessionDead('connecting'), false);
});

test('failed and closed end the session', () => {
  assert.equal(iceSessionDead('failed'), true);
  assert.equal(iceSessionDead('closed'), true);
});

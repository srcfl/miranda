// web/test/pairing-confirm.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  confirmPairingSafety,
  machineAfterConfirmedPairing,
  pendingPairingConfirmation,
} from '../src/pairing/confirm.js';

test('pairing confirmation starts pending and cannot be persisted yet', () => {
  const machine = { machine_id: 'm1', host_pub: '50'.repeat(32), name: 'box' };
  const pending = pendingPairingConfirmation(machine, '162a-f846-b7e7-5584');

  assert.equal(pending.confirmed, false);
  assert.throws(() => machineAfterConfirmedPairing(pending), /not confirmed/);
});

test('explicit confirmation releases the machine for persistence', () => {
  const machine = { machine_id: 'm1', host_pub: '50'.repeat(32), name: 'box' };
  const pending = pendingPairingConfirmation(machine, '162a-f846-b7e7-5584');
  const confirmed = confirmPairingSafety(pending);

  assert.equal(confirmed.confirmed, true);
  assert.equal(machineAfterConfirmedPairing(confirmed), machine);
});

test('missing safety number is rejected before UI confirmation', () => {
  const machine = { machine_id: 'm1', host_pub: '50'.repeat(32), name: 'box' };
  assert.throws(() => pendingPairingConfirmation(machine, ''), /missing safety number/);
});

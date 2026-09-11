// web/test/hello.test.js
import test from 'node:test';
import assert from 'node:assert/strict';
import { applyHello, hideTmuxStrip } from '../src/ui/hello.js';

test('applyHello sets name and engine from the first HELLO', () => {
  const m = applyHello({ machine_id: 'abc', name: 'old' }, { name: 'box', engine: 'herdr' });
  assert.equal(m.name, 'box');
  assert.equal(m.engine, 'herdr');
  assert.equal(m.machine_id, 'abc');
});

test('applyHello keeps engine when a later HELLO is name-only', () => {
  const m = applyHello({ name: 'box', engine: 'herdr' }, { name: 'renamed' });
  assert.equal(m.name, 'renamed');
  assert.equal(m.engine, 'herdr');
});

test('applyHello ignores empty payloads', () => {
  const orig = { name: 'box', engine: 'herdr' };
  assert.equal(applyHello(orig, null), orig);
  assert.equal(applyHello(orig, {}), orig);
  assert.deepEqual(applyHello(orig, { name: '', engine: '' }), orig);
});

test('hideTmuxStrip is herdr-only', () => {
  assert.equal(hideTmuxStrip('herdr'), true);
  assert.equal(hideTmuxStrip(''), false);
  assert.equal(hideTmuxStrip(undefined), false);
  assert.equal(hideTmuxStrip('tmux'), false);
});

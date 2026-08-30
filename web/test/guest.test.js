// web/test/guest.test.js — the SPA guest side (G1e): grant store + sweep, the
// CLI-identical phrasing, the read-only send guard, and source pins on the
// app wiring (join routing, owner routing, no owner affordances on a share).
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import {
  saveGuestGrant, listGuestGrants, guestGrantFor, grantLive, sweepGuestGrants,
  expiryPhrase, shareSummary, guardReadonlySend,
} from '../src/guest.js';
import { FRAME_DATA, FRAME_RESIZE, FRAME_CONTROL } from '../src/noise/frame.js';

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = join(here, '..');
const vector = JSON.parse(readFileSync(join(webRoot, '..', 'testdata', 'grant.json'), 'utf8'));

const vectorGrant = () => JSON.parse(vector.record);

beforeEach(() => {
  const values = new Map();
  globalThis.localStorage = {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
  };
});

// --- phrasing: byte-identical to the CLI ----------------------------------

test('expiryPhrase matches the CLI wording', () => {
  const now = 1_000_000;
  assert.equal(expiryPhrase(now + 30, now), 'expires in under a minute');
  assert.equal(expiryPhrase(now + 42 * 60 + 30, now), 'expires in 42 min');
  assert.equal(expiryPhrase(now + 3 * 3600 + 7 * 60, now), 'expires in 3h 07min');
  assert.equal(expiryPhrase(now - 1, now), 'expired');
});

test('shareSummary matches the CLI list line', () => {
  const now = Math.floor(Date.now() / 1000);
  const g = { ...vectorGrant(), mode: 'ro', na: now + 42 * 60 + 30 };
  assert.equal(shareSummary(g), 'shared with you · read-only · expires in 42 min');
  assert.equal(shareSummary(null), 'shared with you');
});

// --- store + sweep ---------------------------------------------------------

test('guest grants store, pick-latest, and sweep like the CLI', () => {
  const now = Math.floor(Date.now() / 1000);
  const old = { ...vectorGrant(), gid: 'aaaaaaaaaaaaaaaa', machine: 'm1', na: now + 600 };
  const newer = { ...vectorGrant(), gid: 'bbbbbbbbbbbbbbbb', machine: 'm1', na: now + 3600 };
  const dead = { ...vectorGrant(), gid: 'cccccccccccccccc', machine: 'm2', na: now - 3600 };
  for (const g of [old, newer, dead]) saveGuestGrant(g);

  assert.equal(guestGrantFor('m1').gid, 'bbbbbbbbbbbbbbbb');
  assert.equal(guestGrantFor('nope'), null);

  const orphaned = sweepGuestGrants(now);
  assert.deepEqual(orphaned, ['m2']); // only the machine with no live grant
  assert.equal(listGuestGrants().length, 2); // the dead grant is gone
});

test('grantLive requires both a verifying signature and a live window', () => {
  const g = vectorGrant();
  const inWindow = g.nb + 60;
  assert.equal(grantLive(g, inWindow), true);
  assert.equal(grantLive(g, g.na + 1), false); // expired
  assert.equal(grantLive({ ...g, scope: 'other' }, inWindow), false); // tampered
  assert.equal(grantLive(null, inWindow), false);
});

// --- the read-only send guard ---------------------------------------------

test('guardReadonlySend drops data and control, passes resize, survives reconnect reassignment', () => {
  const sent = [];
  const current = {};
  guardReadonlySend(current);

  current.send = (framed) => sent.push(framed[0]); // first connect
  current.send(Uint8Array.of(FRAME_DATA, 0x41));
  current.send(Uint8Array.of(FRAME_CONTROL, 0x7b));
  current.send(Uint8Array.of(FRAME_RESIZE, 0, 80, 0, 24));
  assert.deepEqual(sent, [FRAME_RESIZE]);

  current.send = (framed) => sent.push(framed[0]); // reconnect swaps send — guard must hold
  current.send(Uint8Array.of(FRAME_DATA, 0x42));
  current.send(Uint8Array.of(FRAME_RESIZE, 0, 100, 0, 30));
  assert.deepEqual(sent, [FRAME_RESIZE, FRAME_RESIZE]);

  current.send = null; // closeSession does this; must not throw
  assert.equal(current.send, null);
});

// --- source pins on the app wiring ----------------------------------------

const app = readFileSync(join(webRoot, 'src', 'app.js'), 'utf8');

test('a join link routes before a pairing code', () => {
  const i = app.indexOf("pendingFrag.startsWith('join-')");
  const j = app.indexOf('viewPair(root, pendingFrag, true)');
  assert.ok(i >= 0 && j >= 0 && i < j, 'afterSignIn must branch join- before replaying as a pairing code');
});

test('attach routes a share under the machine owner', () => {
  assert.match(app, /machine\.owner \|\| signer\.address/);
});

test('a read-only share is guarded at session creation', () => {
  const open = app.slice(app.indexOf('function openSession'), app.indexOf('function closeSession'));
  assert.match(open, /guardReadonlySend\(sess\.current\)/);
});

test('a share card carries no owner affordances', () => {
  const shared = app.slice(app.indexOf("className: 'card machine shared'"), app.indexOf("continue;"));
  assert.ok(!shared.includes('retire'), 'shared card must not offer retire');
  assert.match(shared, /shareSummary/);
});

test('the guest chrome hides rename and retire', () => {
  const chrome = app.slice(app.indexOf('function syncGuestChrome'), app.indexOf('function renderChrome'));
  assert.match(chrome, /renameBtn\.hidden = isGuest/);
  assert.match(chrome, /revokeBtn\.hidden = isGuest/);
  assert.match(chrome, /read-only · /);
});

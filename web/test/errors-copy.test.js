// web/test/errors-copy.test.js — the error taxonomy (N3) in the SPA. Source
// pins: every user-facing failure is a plain sentence with a next step, in the
// app's own idiom — no browser alert() anywhere, honest degradation when the
// relay is unreachable, and dead ends that say the way forward.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const app = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../src/app.js'), 'utf8');

test('no browser alert() anywhere in app.js — notices use the sheet idiom', () => {
  assert.doesNotMatch(app, /\balert\(/);
  assert.doesNotMatch(app, /window\.alert/);
  assert.match(app, /function noticeSheet/);
});

test('a failed registry fetch is said out loud, not silently stale', () => {
  assert.match(app, /discoveryPaused/);
  assert.match(app, /The relay is unreachable — showing saved machines/);
  // Any successful fetch clears the notice again.
  assert.match(app, /discoveryPaused = false/);
});

test('rename failures state the fact and the way forward', () => {
  assert.match(app, /The rename was not saved — reload and try again\./);
  assert.match(app, /Names are 1–64 characters with no control characters\./);
});

test('the failed security check explains itself and the next step', () => {
  assert.match(app, /did not pass its signature check/);
  assert.match(app, /Reload to retry; if this keeps happening, sign out and back in\./);
});

test('camera failure offers the alternative path', () => {
  assert.match(app, /allow camera access in your browser settings, or type the code instead/);
});

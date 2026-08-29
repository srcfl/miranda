// web/test/retire.test.js — the machine retirement flow (N2). app.js is
// DOM-heavy, so these are source pins in the pool.test.js idiom: they hold the
// structural guarantees — the confirmation sheet gates the signed revocation,
// a warm session closes first, both entry points share one flow, and the
// plain-words copy stays honest.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const app = readFileSync(join(webRoot, 'src/app.js'), 'utf8');

const retireSheetSrc = app.slice(app.indexOf('function retireSheet'), app.indexOf('function retiredNotice'));

test('revokeMachine is called exactly once — inside the retirement sheet', () => {
  // Confirmation gates the call: the ONLY call site of revokeMachine in the
  // whole app is the sheet's confirm handler. A second call site would mean a
  // path that retires without the sheet's words.
  const calls = [...app.matchAll(/revokeMachine\(/g)].length;
  const imports = [...app.matchAll(/revokeMachine[,}]/g)].length; // the import line
  assert.equal(calls, 1, 'revokeMachine must have exactly one call site');
  assert.ok(imports >= 1, 'revokeMachine still imported');
  assert.match(retireSheetSrc, /revokeMachine\(/);
});

test('retiring closes the warm session before signing the revocation', () => {
  const close = retireSheetSrc.indexOf('closeSession(');
  const revoke = retireSheetSrc.indexOf('revokeMachine(');
  assert.ok(close >= 0 && revoke >= 0, 'both calls live in the sheet');
  assert.ok(close < revoke, 'closeSession must run before revokeMachine');
});

test('the sheet says what happens, what does not, and the way back', () => {
  assert.match(retireSheetSrc, /every device/);
  assert.match(retireSheetSrc, /keeps running/);
  assert.match(retireSheetSrc, /tmux sessions/);
  assert.match(retireSheetSrc, /pair fresh/);
  // The destructive button is the explicit one, and there is a way out.
  assert.match(retireSheetSrc, /btn danger/);
  assert.match(retireSheetSrc, /cancel/);
});

test('no blocking browser modals remain for retirement', () => {
  assert.doesNotMatch(app, /window\.confirm\(/);
  assert.doesNotMatch(app, /window\.alert\(/);
});

test('both entry points use the one retirement flow', () => {
  // The machine list card offers retire…
  const renderSrc = app.slice(app.indexOf('function renderMachines'), app.indexOf('function viewMachines'));
  assert.match(renderSrc, /retireSheet\(/);
  // …and the terminal view's ⊘ goes through the same sheet.
  const termSrc = app.slice(app.indexOf('function viewTerminal'));
  assert.match(termSrc, /retireSheet\(view, m\(\)/);
});

test('after retiring, the list and the empty state point the way back', () => {
  const noticeSrc = app.slice(app.indexOf('function retiredNotice'), app.indexOf('function renderMachines'));
  assert.match(noticeSrc, /run `mir up` on it and pair fresh/);
  // Publication failure is surfaced as a heads-up, not silence — the retire
  // itself held locally (revokeMachine persists before any network).
  assert.match(noticeSrc, /Heads-up/);
  const emptySrc = app.slice(app.indexOf('function emptyMachinesView'), app.indexOf('function pollForMachine'));
  assert.match(emptySrc, /retiredNotice\(\)/);
  const renderSrc = app.slice(app.indexOf('function renderMachines'), app.indexOf('function viewMachines'));
  assert.match(renderSrc, /retiredNotice\(\)/);
});

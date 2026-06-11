// web/test/keybar.test.js
// Pure-helper tests for the mobile keyboard accessory bar. The bar's byte
// output and its sticky-Ctrl state machine are pure and DOM-free so they can be
// unit-tested here; the DOM rendering / send wiring lives in keybar.js and is
// exercised by the app at runtime.
import test from 'node:test';
import assert from 'node:assert/strict';
import { ctrlByte, KEY_BYTES, keyBytes, stickyCtrl } from '../src/ui/keybar.js';

const td = new TextDecoder();
const decode = (u) => (u ? td.decode(u) : u);

test('ctrlByte maps letters to their control codes (letter & 0x1f)', () => {
  assert.equal(ctrlByte('c'), 0x03); // Ctrl-C / SIGINT
  assert.equal(ctrlByte('a'), 0x01); // Ctrl-A
  assert.equal(ctrlByte('z'), 0x1a); // Ctrl-Z
  assert.equal(ctrlByte('d'), 0x04); // Ctrl-D / EOF
});

test('ctrlByte is case-insensitive for letters', () => {
  assert.equal(ctrlByte('C'), 0x03);
  assert.equal(ctrlByte('C'), ctrlByte('c'));
});

test('ctrlByte handles the non-letter control symbols', () => {
  // The classic Ctrl-symbol mappings: @ [ \ ] ^ _ and ? -> 0x00..0x1f / 0x7f.
  assert.equal(ctrlByte('@'), 0x00); // NUL
  assert.equal(ctrlByte(' '), 0x00); // Ctrl-Space == NUL
  assert.equal(ctrlByte('['), 0x1b); // ESC
  assert.equal(ctrlByte('\\'), 0x1c);
  assert.equal(ctrlByte(']'), 0x1d);
  assert.equal(ctrlByte('^'), 0x1e);
  assert.equal(ctrlByte('_'), 0x1f);
  assert.equal(ctrlByte('?'), 0x7f); // Ctrl-? == DEL
});

test('ctrlByte returns null for characters with no control mapping', () => {
  assert.equal(ctrlByte('1'), null);
  assert.equal(ctrlByte(''), null);
  assert.equal(ctrlByte('hi'), null); // only single chars map
  assert.equal(ctrlByte(undefined), null);
});

test('named keys emit the exact terminal byte sequences', () => {
  assert.deepEqual(KEY_BYTES.esc, new Uint8Array([0x1b]));
  assert.deepEqual(KEY_BYTES.tab, new Uint8Array([0x09]));
  assert.deepEqual(KEY_BYTES.up, new Uint8Array([0x1b, 0x5b, 0x41])); // ESC [ A
  assert.deepEqual(KEY_BYTES.down, new Uint8Array([0x1b, 0x5b, 0x42])); // ESC [ B
  assert.deepEqual(KEY_BYTES.right, new Uint8Array([0x1b, 0x5b, 0x43])); // ESC [ C
  assert.deepEqual(KEY_BYTES.left, new Uint8Array([0x1b, 0x5b, 0x44])); // ESC [ D
});

test('arrow sequences decode to the canonical ANSI escapes', () => {
  assert.equal(decode(KEY_BYTES.up), '\x1b[A');
  assert.equal(decode(KEY_BYTES.down), '\x1b[B');
  assert.equal(decode(KEY_BYTES.right), '\x1b[C');
  assert.equal(decode(KEY_BYTES.left), '\x1b[D');
});

test('keyBytes resolves named keys and literal characters to bytes', () => {
  assert.deepEqual(keyBytes('esc'), new Uint8Array([0x1b]));
  assert.deepEqual(keyBytes('tab'), new Uint8Array([0x09]));
  assert.deepEqual(keyBytes('up'), new Uint8Array([0x1b, 0x5b, 0x41]));
  // literal extras are UTF-8 encoded
  assert.deepEqual(keyBytes('|'), new Uint8Array([0x7c]));
  assert.deepEqual(keyBytes('/'), new Uint8Array([0x2f]));
  assert.deepEqual(keyBytes('~'), new Uint8Array([0x7e]));
  assert.deepEqual(keyBytes('-'), new Uint8Array([0x2d]));
});

test('keyBytes returns null for an unknown token', () => {
  assert.equal(keyBytes('nope'), null);
});

// --- sticky Ctrl state machine -------------------------------------------
// stickyCtrl() is a tiny pure controller: arm() flips it on, press(token)
// consumes the next key — control-mapping it when armed, then disarming — and
// returns the bytes to send (or null). No DOM, no send ref.

test('disarmed: press passes named/arrow keys through unchanged', () => {
  const s = stickyCtrl();
  assert.equal(s.armed, false);
  assert.deepEqual(s.press('esc'), new Uint8Array([0x1b]));
  assert.deepEqual(s.press('up'), new Uint8Array([0x1b, 0x5b, 0x41]));
  assert.deepEqual(s.press('c'), new Uint8Array([0x63])); // plain 'c'
  assert.equal(s.armed, false);
});

test('arm -> next char is ctrl-mapped -> auto-disarms', () => {
  const s = stickyCtrl();
  s.arm();
  assert.equal(s.armed, true);
  assert.deepEqual(s.press('c'), new Uint8Array([0x03])); // Ctrl-C
  assert.equal(s.armed, false, 'Ctrl disarms after one key');
  // the key after that is a plain key again
  assert.deepEqual(s.press('c'), new Uint8Array([0x63]));
});

test('arm applies to a single arbitrary character key', () => {
  const s = stickyCtrl();
  s.arm();
  assert.deepEqual(s.press('a'), new Uint8Array([0x01])); // Ctrl-A
  assert.equal(s.armed, false);
});

test('arm + a non-mappable key sends nothing and disarms (no junk byte)', () => {
  const s = stickyCtrl();
  s.arm();
  // '1' has no control code; arming then pressing it must not emit a literal '1'
  assert.equal(s.press('1'), null);
  assert.equal(s.armed, false);
});

test('Ctrl + a named arrow falls back to the plain arrow sequence and disarms', () => {
  // Arrows have no single-char control code; a dropped keystroke is worse than
  // an unmodified arrow, so send the plain arrow sequence and disarm.
  const s = stickyCtrl();
  s.arm();
  assert.deepEqual(s.press('up'), new Uint8Array([0x1b, 0x5b, 0x41]));
  assert.equal(s.armed, false);
});

test('toggle() arms then disarms without consuming a key', () => {
  const s = stickyCtrl();
  s.toggle();
  assert.equal(s.armed, true);
  s.toggle();
  assert.equal(s.armed, false);
  // pressing after a clean toggle-off is a plain key
  assert.deepEqual(s.press('c'), new Uint8Array([0x63]));
});

test('arm is idempotent and onChange fires on state transitions', () => {
  const seen = [];
  const s = stickyCtrl({ onChange: (a) => seen.push(a) });
  s.arm();
  s.arm(); // already armed; still fine
  assert.equal(s.armed, true);
  s.press('c'); // consumes -> disarms
  assert.equal(s.armed, false);
  // at least one true and a trailing false were observed
  assert.ok(seen.includes(true));
  assert.equal(seen[seen.length - 1], false);
});

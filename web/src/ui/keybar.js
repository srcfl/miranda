// web/src/ui/keybar.js — mobile keyboard accessory bar.
//
// On a phone you can't comfortably reach Esc, Ctrl-C, Tab, or the arrows from
// the soft keyboard. This module renders a thumb-friendly row of buttons above
// the terminal that send the right control bytes, plus a sticky Ctrl modifier
// (tap Ctrl, then a key, to send its control code).
//
// The byte logic is split out as pure, DOM-free helpers (ctrlByte / KEY_BYTES /
// keyBytes / stickyCtrl) so it can be unit-tested without a browser. The bar
// itself stays transport-agnostic: it hands the caller the RAW key bytes via a
// sendRaw callback, and the app frames + ships them through the terminal's own
// current.send (read LIVE each press) — so a bar press is byte-identical to a
// typed key and survives reconnects exactly like term.onData.

const te = new TextEncoder();

// ctrlByte maps a single character to its ASCII control code, or null if the
// character has no control mapping. Letters fold to `ch & 0x1f` (so 'c' -> 0x03,
// 'a' -> 0x01); the classic non-letter control keys map per the C0 table. This
// mirrors what a real terminal sends when you hold Ctrl and press the key.
export function ctrlByte(ch) {
  if (typeof ch !== 'string' || ch.length !== 1) return null;
  const c = ch.toLowerCase();
  const code = c.charCodeAt(0);
  if (code >= 97 && code <= 122) return code & 0x1f; // a..z -> 0x01..0x1a
  switch (ch) {
    case '@':
    case ' ': return 0x00; // Ctrl-@ / Ctrl-Space -> NUL
    case '[': return 0x1b; // ESC
    case '\\': return 0x1c; // FS
    case ']': return 0x1d; // GS
    case '^': return 0x1e; // RS
    case '_': return 0x1f; // US
    case '?': return 0x7f; // DEL
    default: return null;
  }
}

// KEY_BYTES holds the fixed byte sequences for the named (non-character) keys.
// Arrows are the canonical ANSI cursor escapes (ESC [ A/B/C/D).
export const KEY_BYTES = {
  esc: new Uint8Array([0x1b]),
  tab: new Uint8Array([0x09]),
  up: new Uint8Array([0x1b, 0x5b, 0x41]), // ESC [ A
  down: new Uint8Array([0x1b, 0x5b, 0x42]), // ESC [ B
  right: new Uint8Array([0x1b, 0x5b, 0x43]), // ESC [ C
  left: new Uint8Array([0x1b, 0x5b, 0x44]), // ESC [ D
};

// keyBytes resolves a token to the bytes to send: a named key (esc/tab/arrows)
// returns its fixed sequence; a single literal character is UTF-8 encoded (so
// the extras |, /, ~, - work). Returns null for an unknown multi-char token.
export function keyBytes(token) {
  if (KEY_BYTES[token]) return KEY_BYTES[token];
  if (typeof token === 'string' && token.length === 1) return te.encode(token);
  return null;
}

// stickyCtrl is the sticky-modifier state machine, kept pure so it can be tested
// without the DOM. arm()/toggle() flip the armed flag; press(token) consumes one
// key and returns the bytes to send:
//   - armed + a character with a control code -> the control byte, then disarm.
//   - armed + a key with NO control code (arrows, or a non-mappable char) ->
//     fall back to the plain key bytes and disarm (a dropped keystroke is worse
//     than an unmodified one; a non-mappable char yields null = send nothing).
//   - disarmed -> the plain key bytes, unchanged.
// onChange(armed) (optional) fires whenever the armed flag transitions, so the
// UI can re-style the Ctrl button.
export function stickyCtrl({ onChange } = {}) {
  let armed = false;
  const set = (v) => { if (v !== armed) { armed = v; onChange && onChange(armed); } };
  const api = {
    get armed() { return armed; },
    arm() { set(true); },
    disarm() { set(false); },
    toggle() { set(!armed); },
    press(token) {
      if (!armed) return keyBytes(token);
      set(false); // Ctrl is sticky for exactly one key
      if (typeof token === 'string' && token.length === 1) {
        const b = ctrlByte(token);
        return b == null ? null : new Uint8Array([b]);
      }
      // named key (arrow/esc/tab) under Ctrl: no single-char control code — send
      // the plain sequence rather than drop the press.
      return keyBytes(token);
    },
  };
  return api;
}

// --- DOM bar --------------------------------------------------------------
// shouldShowKeybar gates the bar on a coarse pointer (touch). Desktop users
// keep a clean terminal. Defaults to window.matchMedia; injectable for tests.
export function shouldShowKeybar(mm = (typeof window !== 'undefined' ? window.matchMedia : null)) {
  try { return !!(mm && mm('(pointer: coarse)').matches); } catch { return false; }
}

// Button layout: [token, label]. A token is a named key, a literal char, or the
// special 'ctrl' (the sticky modifier toggle).
const KEYS = [
  ['esc', 'Esc'],
  ['ctrl', 'Ctrl'],
  ['tab', 'Tab'],
  ['up', '↑'],
  ['down', '↓'],
  ['left', '←'],
  ['right', '→'],
  ['|', '|'],
  ['/', '/'],
  ['~', '~'],
  ['-', '-'],
];

// makeKeybar builds the accessory bar element.
//   sendRaw  — (Uint8Array) => void. Called with the RAW key bytes for each
//              press. The APP supplies this and does the framing through the
//              terminal's own send path (current.send(encodeData(bytes))), so a
//              keystroke from the bar is byte-identical to one typed into xterm
//              and rides the same reconnect-safe current.send. Read live there —
//              do NOT capture a stale send ref.
//   focus    — optional () => refocus the terminal (keep the soft keyboard up).
// Returns { el, sticky }: `el` is the bar node (caller inserts it) and `sticky`
// is the shared state machine, exposed so a physical-keyboard handler can also
// consume an armed Ctrl.
export function makeKeybar(sendRaw, focus) {
  const doc = document;
  const bar = doc.createElement('div');
  bar.className = 'keybar';
  bar.setAttribute('role', 'toolbar');
  bar.setAttribute('aria-label', 'terminal keys');

  let ctrlBtn = null;
  const sticky = stickyCtrl({
    onChange: (a) => { if (ctrlBtn) ctrlBtn.classList.toggle('armed', a); },
  });

  const emit = (bytes) => { if (bytes && bytes.length && sendRaw) sendRaw(bytes); };

  const press = (token) => {
    if (token === 'ctrl') { sticky.toggle(); focus && focus(); return; }
    emit(sticky.press(token));
    focus && focus();
  };

  const arrowAria = { up: 'up arrow', down: 'down arrow', left: 'left arrow', right: 'right arrow' };
  for (const [token, label] of KEYS) {
    const b = doc.createElement('button');
    b.className = 'keybar-btn' + (token === 'ctrl' ? ' keybar-ctrl' : '');
    b.type = 'button';
    b.textContent = label;
    b.setAttribute('aria-label', arrowAria[token] || label);
    // Never steal focus from the terminal: keep the soft keyboard up. tabindex=-1
    // keeps the buttons out of the tab order, and preventDefault on pointerdown
    // stops the focus shift before it happens (so term.textarea stays focused).
    b.tabIndex = -1;
    b.addEventListener('pointerdown', (e) => { e.preventDefault(); press(token); });
    // Fallback for environments without Pointer Events (older mobile Safari).
    b.addEventListener('mousedown', (e) => e.preventDefault());
    if (token === 'ctrl') ctrlBtn = b;
    bar.append(b);
  }
  return { el: bar, sticky };
}

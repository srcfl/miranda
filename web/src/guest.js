// web/src/guest.js — the guest side of sharing in the SPA (G1e): the local
// grant store, the shared expiry phrasing, and the read-only send guard.
// Mirrors the CLI's client/shares.go where the shapes overlap.
import { FRAME_RESIZE } from './noise/frame.js';
import { verifyGrant, validAt } from './identity/grant.js';

const KEY = 'tr_guest_grants';
const SKEW_S = 300; // matches identity.GrantSkew

function readAll() {
  try {
    return JSON.parse(localStorage.getItem(KEY) || '{}');
  } catch {
    return {};
  }
}

function writeAll(map) {
  try {
    localStorage.setItem(KEY, JSON.stringify(map));
  } catch {}
}

// saveGuestGrant stores a verified grant record (the caller verified it).
export function saveGuestGrant(sg) {
  const map = readAll();
  map[sg.gid] = sg;
  writeAll(map);
}

export function listGuestGrants() {
  return Object.values(readAll()).sort((a, b) => b.na - a.na);
}

// guestGrantFor returns the latest-expiring grant covering machineID, or null.
export function guestGrantFor(machineID) {
  for (const g of listGuestGrants()) {
    if (g.machine === machineID) return g;
  }
  return null;
}

// grantLive reports whether a grant's window covers now and it still verifies.
export function grantLive(g, nowSec = Math.floor(Date.now() / 1000)) {
  return !!g && verifyGrant(g) && validAt(g, nowSec);
}

// sweepGuestGrants drops grants whose window has fully closed (past na + skew)
// and returns the machine ids left with no grant at all — the caller removes
// those machine entries, exactly like the CLI's SweepGuestState.
export function sweepGuestGrants(nowSec = Math.floor(Date.now() / 1000)) {
  const map = readAll();
  const hadMachine = new Set();
  const liveMachine = new Set();
  for (const [gid, g] of Object.entries(map)) {
    hadMachine.add(g.machine);
    if (g.na < nowSec - SKEW_S) delete map[gid];
    else liveMachine.add(g.machine);
  }
  writeAll(map);
  return [...hadMachine].filter((m) => !liveMachine.has(m));
}

// expiryPhrase matches the CLI's wording exactly ("expires in 42 min").
export function expiryPhrase(na, nowSec = Math.floor(Date.now() / 1000)) {
  const left = na - nowSec;
  if (left <= 0) return 'expired';
  if (left < 60) return 'expires in under a minute';
  if (left < 3600) return `expires in ${Math.floor(left / 60)} min`;
  return `expires in ${Math.floor(left / 3600)}h ${String(Math.floor(left / 60) % 60).padStart(2, '0')}min`;
}

export function modeWord(mode) {
  return mode === 'rw' ? 'read-write' : 'read-only';
}

// shareSummary is the one line a share renders under its name — identical to
// the CLI's `mir ls` phrasing.
export function shareSummary(grant) {
  if (!grant) return 'shared with you';
  return `shared with you · ${modeWord(grant.mode)} · ${expiryPhrase(grant.na)}`;
}

// guardReadonlySend makes a session's send path drop everything except RESIZE
// before it reaches the wire. connectOnce assigns current.send on every
// (re)connect, so the guard is a property setter: every assignment flows
// through it, and no keystroke source (term.onData, the key bar, tmux control)
// can bypass it — they all call current.send. The agent drops guest input
// anyway (G1c); this keeps the honest client from even sending it.
export function guardReadonlySend(current) {
  let inner = current.send || null;
  Object.defineProperty(current, 'send', {
    get() {
      if (!inner) return inner;
      return (framed) => {
        if (framed && framed[0] !== FRAME_RESIZE) return; // ro: output only
        inner(framed);
      };
    },
    set(fn) {
      inner = fn;
    },
  });
}

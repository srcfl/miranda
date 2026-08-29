// web/src/net/pool.js — warm multi-machine policy (R2), DOM-free.
//
// One browser tab holds several live machines. This module owns the decisions;
// app.js owns the DOM. Three pieces:
//
//   makePool       — which machines stay warm: a bounded LRU set. Activating a
//                    machine joins/refreshes it; overflow names the
//                    least-recently-used BACKGROUND machine to close. The active
//                    machine is never evicted.
//   makeParkPolicy — battery honesty: with ICE keepalive at 500 ms, N warm
//                    sessions cost real radio time on a phone. When the tab has
//                    been hidden for a while, background sessions are parked
//                    (transports closed, terminal kept). The ACTIVE machine is
//                    never parked — the OS already governs a hidden tab, and on
//                    desktop a hidden-but-running tab should keep its live
//                    terminal. A parked machine redials the moment it is
//                    switched to; the pool resumes when the tab returns.
//   stateView      — one place mapping a session's connection state to what the
//                    UI shows (the R1 status pill for the active machine, the
//                    strip chip for every pooled one), so the mapping is pinned
//                    by tests instead of living inline in view code.
export function makePool({ max = 3, now = Date.now } = {}) {
  const lastUsed = new Map(); // machine_id -> ms; insertion order irrelevant, ts decides
  let active = null;

  return {
    // activate makes id the active machine (joining the pool if new) and
    // returns { evict: [ids] } — background machines the caller must close.
    activate(id) {
      lastUsed.set(id, now());
      active = id;
      const evict = [];
      while (lastUsed.size > max) {
        let lru = null;
        for (const [k, t] of lastUsed) {
          if (k === active) continue;
          if (lru === null || t < lastUsed.get(lru)) lru = k;
        }
        if (lru === null) break; // max<1 pathological: keep the active anyway
        lastUsed.delete(lru);
        evict.push(lru);
      }
      return { evict };
    },
    drop(id) { lastUsed.delete(id); if (active === id) active = null; },
    has: (id) => lastUsed.has(id),
    ids: () => [...lastUsed.keys()],
    activeId: () => active,
    size: () => lastUsed.size,
  };
}

// Parked-when-hidden policy. Injectable timers keep it unit-testable.
// visibility(hidden) is fed every visibilitychange; after hiddenDelayMs of
// uninterrupted hidden the policy parks (onPark), and the next return to
// visible resumes (onResume). A quick app-switch shorter than the delay costs
// nothing in either direction.
export const HIDDEN_PARK_MS = 25000;

export function makeParkPolicy({ hiddenDelayMs = HIDDEN_PARK_MS, onPark, onResume,
  setTimer = setTimeout, clearTimer = clearTimeout } = {}) {
  let timer = null;
  let parked = false;
  return {
    visibility(hidden) {
      if (hidden) {
        if (timer === null && !parked) {
          timer = setTimer(() => { timer = null; parked = true; onPark && onPark(); }, hiddenDelayMs);
        }
        return;
      }
      if (timer !== null) { clearTimer(timer); timer = null; }
      if (parked) { parked = false; onResume && onResume(); }
    },
    isParked: () => parked,
    dispose() { if (timer !== null) { clearTimer(timer); timer = null; } },
  };
}

// stateView maps one machine session's state to UI strings. `sess` fields:
//   conn: 'connecting'|'connected'|'reconnecting'|'failed'  (runSession state)
//   degraded: bool   (R1 link honesty: ICE flip seen on the live session)
//   parked: bool     (park policy above)
//   attempt: number  (reconnect attempt, for the pill)
// The pill strings are R1's, verbatim — the active machine's pill must not
// change because R2 moved the mapping here.
export function stateView(sess) {
  if (sess.parked) return { pill: '⏸ parked', cls: 'wait', chip: 'parked' };
  if (sess.conn === 'connected') {
    return sess.degraded
      ? { pill: '⟳ resuming', cls: 'wait', chip: 'wait' }
      : { pill: '● live', cls: 'ok', chip: 'live' };
  }
  if (sess.conn === 'reconnecting') {
    return { pill: '⟳ reconnecting' + (sess.attempt ? ' (' + sess.attempt + ')' : ''), cls: 'wait', chip: 'wait' };
  }
  if (sess.conn === 'failed') return { pill: '⊘ tap to retry', cls: 'failed', chip: 'failed' };
  return { pill: '⟳ connecting', cls: 'wait', chip: 'wait' };
}

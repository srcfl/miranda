// Early reaction to a degraded link (R1). Browsers report `disconnected` within
// a second or two of a network flip but can take 10 s+ to escalate it to
// `failed` — and only `failed` tears the session down (see ice-state.js). That
// gap is a frozen terminal with a pill still claiming "live". makeDisconnectGrace
// closes the gap: on `disconnected` it reports degraded immediately and arms a
// grace timer; if the link is not back within graceMs it calls kill() so the
// caller ends the session NOW and the reconnect loop redials promptly. Recovery
// inside the window disarms the timer, so a blip that heals costs nothing.
//
// DISCONNECT_GRACE_MS balances two costs: tearing down a link that was about to
// heal by itself (rebuild ≈ backoff(0) ≤ 500 ms + one connect) vs sitting frozen.
// The R1 budget is: disconnected detection (~1 s) + grace (1.2 s) + backoff(0)
// (≤ 0.5 s) + connect (~1 s) < 3 s-ish resume — reconnect.test.js pins the sum.
//
// DOM-free: timers are injectable, so the policy is unit-testable.
export const DISCONNECT_GRACE_MS = 1200;

export function makeDisconnectGrace({ graceMs = DISCONNECT_GRACE_MS, kill, onDegraded, onRecovered,
  setTimer = setTimeout, clearTimer = clearTimeout } = {}) {
  let timer = null;
  let degraded = false;
  const disarm = () => { if (timer !== null) { clearTimer(timer); timer = null; } };
  return {
    // Feed every PeerConnection connectionState change here. failed/closed are
    // the dead path (iceSessionDead tears down elsewhere): just disarm so the
    // grace timer cannot fire into an already-dead session.
    onState(state) {
      if (state === 'connected') {
        disarm();
        if (degraded) { degraded = false; onRecovered && onRecovered(); }
      } else if (state === 'disconnected') {
        if (!degraded) { degraded = true; onDegraded && onDegraded(); }
        if (timer === null) timer = setTimer(() => { timer = null; kill && kill(); }, graceMs);
      } else if (state === 'failed' || state === 'closed') {
        disarm();
      }
    },
    dispose: disarm,
  };
}

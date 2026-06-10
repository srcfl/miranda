// runSession drives reconnection for one attach. It is DOM-free: every effect is
// injected, so the policy is unit-testable. connectOnce(onConnected) must RESOLVE
// when an established session ENDS (a drop) and REJECT if it never connected (a
// setup failure); it must call onConnected() once the channel is live.
//
//   onState(state, attempt) state in connecting|connected|reconnecting|failed
//   sleep(ms) -> Promise ; backoffFor(attempt) -> ms ; maxSetupAttempts -> int
//
// The first attempt is unconditional and reconnects retry with backoff. We do NOT
// gate on page visibility: mobile browsers already throttle background-tab timers, so
// the capped backoff is enough to avoid churn — and gating broke the first connect
// when the tab reported hidden. Revisit an explicit visibility pause if users report
// background battery drain. Returns { stop, retryNow }.
export function runSession({ connectOnce, onState, sleep, backoffFor, maxSetupAttempts = 6 }) {
  let stopped = false;
  let everConnected = false;
  let attempt = 0;
  let retryGate = null; // resolve fn while parked in the failed state

  const retryNow = () => { const r = retryGate; retryGate = null; if (r) r(); };

  (async () => {
    while (!stopped) {
      onState(everConnected ? 'reconnecting' : 'connecting', attempt);
      try {
        await connectOnce(() => { everConnected = true; attempt = 0; onState('connected', 0); });
        // resolved => an established session ended. Fall through to backoff + retry.
      } catch {
        if (!everConnected && ++attempt >= maxSetupAttempts) {
          onState('failed', attempt);
          await new Promise((res) => { retryGate = res; }); // wait for retryNow() or stop()
          everConnected = false; attempt = 0;
          continue; // retry immediately
        }
      }
      if (stopped) break;
      // attempt grows only on setup failures (the catch above); a transient drop keeps
      // attempt at 0 so the first reconnect shows a clean "reconnecting…" + terminal line.
      // backoffFor(0)'s jitter already prevents a tight loop on a flapping link.
      await sleep(backoffFor(attempt));
    }
  })();

  return {
    stop: () => { stopped = true; retryNow(); },
    retryNow,
  };
}

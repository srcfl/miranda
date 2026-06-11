// runSession drives reconnection for one attach. It is DOM-free: every effect is
// injected, so the policy is unit-testable. connectOnce(onConnected) must RESOLVE
// when an established session ENDS (a drop) and REJECT if it never connected (a
// setup failure); it must call onConnected() once the channel is live.
//
//   onState(state, failures) state in connecting|connected|reconnecting|failed
//   sleep(ms) -> Promise ; backoffFor(failures) -> ms ; maxAttempts -> int
//   now() -> ms epoch (default Date.now, injectable) ; minHealthyMs -> int
//
// Storm fix: the give-up branch must fire REGARDLESS of whether a session ever
// connected. Earlier code only counted *setup* failures and reset the counter the
// moment a session connected, so once a live session existed the give-up test was
// unreachable: an agent that died after a live session rejected every reconnect with
// the counter pinned at 0, retrying at backoffFor(0) (0–500ms jitter) forever — a
// 2–4/sec storm. Instead we track `failures` = consecutive connect attempts that did
// NOT yield a *healthy* session, and:
//   - a setup failure (reject) or a FLAP (connected but dropped before minHealthyMs)
//     => failures++  => growing backoff;
//   - a HEALTHY drop (was up >= minHealthyMs) => failures=0 => prompt reconnect
//     (backoffFor(0)), since a long-lived session that dropped is a normal event, not
//     a failing endpoint;
//   - failures >= maxAttempts => onState('failed', failures), then PARK until
//     retryNow()/stop() — so a permanently-gone agent shows "tap to retry" instead of
//     storming.
//
// We do NOT gate on page visibility: mobile browsers already throttle background-tab
// timers, so the capped backoff is enough to avoid churn — and gating broke the first
// connect when the tab reported hidden. Revisit an explicit visibility pause if users
// report background battery drain. Returns { stop, retryNow }.
export function runSession({ connectOnce, onState, sleep, backoffFor, maxAttempts = 6, now = Date.now, minHealthyMs = 5000 }) {
  let stopped = false;
  let everConnected = false;
  let failures = 0;
  let retryGate = null; // resolve fn while parked in the failed state

  const retryNow = () => { const r = retryGate; retryGate = null; if (r) r(); };

  (async () => {
    while (!stopped) {
      onState(everConnected ? 'reconnecting' : 'connecting', failures);
      let connectedAt = null; // set when this attempt reports a live channel
      try {
        await connectOnce(() => { everConnected = true; connectedAt = now(); onState('connected', 0); });
        // resolved => an established session ended. Healthy (up >= minHealthyMs) drops
        // reset failures; a brief flap counts as a failure so the link can't churn.
        if (connectedAt !== null && now() - connectedAt >= minHealthyMs) failures = 0;
        else failures++;
      } catch {
        // never reached a live channel (or onConnected fired then threw before we
        // could time it): a setup failure -> count it toward giving up.
        failures++;
      }
      if (stopped) break;

      if (failures >= maxAttempts) {
        onState('failed', failures);
        await new Promise((res) => { retryGate = res; }); // park: wait for retryNow()/stop()
        if (stopped) break;
        everConnected = false; failures = 0;
        continue; // user asked to retry: start clean
      }
      // failures==0 (healthy drop) -> backoffFor(0) is a prompt reconnect; >0 grows it.
      await sleep(backoffFor(failures));
    }
  })();

  return {
    stop: () => { stopped = true; retryNow(); },
    retryNow,
  };
}

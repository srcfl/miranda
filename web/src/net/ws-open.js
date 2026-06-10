// awaitSocketOpen resolves once a WebSocket is open and rejects if it errors,
// capturing the outcome SYNCHRONOUSLY: it guards `readyState === OPEN` up front, so
// it is safe even when the socket has already opened by the time it is called.
//
// This exists because of a real bug: attach() creates the signalling socket and then
// `await`s an unrelated fetch (iceServers) before subscribing to 'open'. On a fast
// path — localhost, or a nearby relay — the socket opens DURING that await, the
// one-shot 'open' event fires with no listener attached, and a handler added
// afterwards never sees it, hanging attach() forever at 'ws-connecting'. The
// readyState guard makes the hang impossible regardless of when this is called.
//
// Kept dependency-free (no DOM/xterm imports) so it is unit-testable under node.
export function awaitSocketOpen(ws) {
  return new Promise((resolve, reject) => {
    if (ws.readyState === 1 /* WebSocket.OPEN */) return resolve();
    ws.addEventListener('open', () => resolve(), { once: true });
    ws.addEventListener('error', () => reject(new Error('signal socket error')), { once: true });
  });
}

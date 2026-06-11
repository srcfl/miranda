// web/src/boot.js — external bootstrap so the page needs no inline module script
// (keeps CSP script-src tight: 'self' + a per-request nonce only on the import map).
import { start } from './app.js';
start(document.getElementById('app'));

// Offline app shell. The worker is network-first, so the relay's no-store
// freshness guarantee holds whenever it is reachable; the cache only serves
// when it is not, letting the installed PWA open while the relay is down.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// web/sw.js — offline app shell (network-first, cache fallback).
//
// The SPA is a client-code trust root, so the relay serves everything
// Cache-Control: no-store — freshness over stale trusted-code delivery. This
// worker preserves that property: every request goes to the NETWORK FIRST and
// the cache is rewritten from each fresh response; the cache is only READ when
// the relay is unreachable. While the relay is up you always run exactly the
// code it serves; while it is down the installed app still opens and the
// reconnect loop dials until signaling returns.
//
// Recovery is not pinnable by a stale/compromised worker: browsers fetch sw.js
// update checks from the origin (this worker cannot intercept them), and the
// relay serves sw.js no-store — so a new deploy replaces this worker on the
// next online load.

const CACHE = 'mir-shell-v1';

// Everything the app needs to boot. test/sw.test.js fails if this list drifts
// from the files on disk — add new modules here when you add them to src/.
const SHELL = [
  '/',
  '/manifest.json',
  '/icons/apple-touch-icon.png',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
  '/icons/icon-maskable-512.png',
  '/src/app.js',
  '/src/boot.js',
  '/src/identity.js',
  '/src/identity/auth.js',
  '/src/identity/binding.js',
  '/src/identity/owner.js',
  '/src/identity/registry.js',
  '/src/identity/wallet.js',
  '/src/net/backoff.js',
  '/src/net/reconnect.js',
  '/src/net/ws-open.js',
  '/src/noise/frame.js',
  '/src/noise/noise-kk.js',
  '/src/pair.js',
  '/src/pairing/code.js',
  '/src/pairing/confirm.js',
  '/src/pairing/nnpsk0.js',
  '/src/pairing/sas.js',
  '/src/registry.js',
  '/src/rp.js',
  '/src/store.js',
  '/src/ui/keybar.js',
  '/src/wallet/base58.js',
  '/src/wallet/bip39.js',
  '/src/wallet/slip10.js',
  '/src/wallet/wordlist.js',
  '/vendor/jsqr.js',
  '/vendor/noble-ciphers-chacha.js',
  '/vendor/noble-curves-ed25519.js',
  '/vendor/noble-hashes-hkdf.js',
  '/vendor/noble-hashes-hmac.js',
  '/vendor/noble-hashes-pbkdf2.js',
  '/vendor/noble-hashes-sha2.js',
  '/vendor/noble-hashes-utils.js',
  '/vendor/xterm-addon-fit.js',
  '/vendor/xterm.css',
  '/vendor/xterm.js',
];

// Live conversations with the relay — never cached, never intercepted.
// (WebSocket upgrades bypass the fetch handler anyway; this covers the plain
// HTTP ones like /turn-credentials and keeps the list in one place.)
const SIGNALING = ['/agent/signal', '/attach', '/pair', '/turn-credentials', '/healthz'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (SIGNALING.includes(url.pathname)) return;
  // Any navigation serves the SPA entry, matching the server's routing.
  const key = req.mode === 'navigate' ? '/' : url.pathname;
  event.respondWith(
    fetch(req)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((cache) => cache.put(key, copy)).catch(() => {});
        }
        return res;
      })
      .catch(() =>
        caches.match(key).then(
          (hit) => hit || new Response('offline', { status: 503, headers: { 'Content-Type': 'text/plain' } }),
        ),
      ),
  );
});

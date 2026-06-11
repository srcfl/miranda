// Service-worker behavior: the offline shell must stay NETWORK-FIRST (the
// relay's no-store freshness guarantee is the security posture — cache is a
// fallback, never preferred), the precache list must match the files on disk,
// and signaling endpoints must never be intercepted.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join, dirname } from 'node:path';
import vm from 'node:vm';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const ORIGIN = 'https://relay.test';

// Evaluate the real sw.js in a sandbox with an in-memory Cache API.
function loadWorker(fetchImpl) {
  const handlers = {};
  const stores = new Map(); // cache name -> Map(key -> Response)
  const cacheFor = (name) => {
    if (!stores.has(name)) stores.set(name, new Map());
    const entries = stores.get(name);
    return {
      addAll: async (paths) => {
        for (const p of paths) entries.set(p, await sandbox.fetch(p));
      },
      put: async (key, res) => void entries.set(key, res),
      match: async (key) => entries.get(key),
    };
  };
  const caches = {
    open: async (name) => cacheFor(name),
    keys: async () => [...stores.keys()],
    delete: async (name) => stores.delete(name),
    match: async (key) => {
      for (const entries of stores.values()) if (entries.has(key)) return entries.get(key);
      return undefined;
    },
  };
  const sandbox = {
    self: {
      addEventListener: (type, fn) => void (handlers[type] = fn),
      skipWaiting: () => {},
      clients: { claim: () => {} },
      location: new URL(ORIGIN + '/sw.js'),
    },
    caches,
    fetch: fetchImpl,
    URL,
    Response,
    console,
  };
  vm.createContext(sandbox);
  vm.runInContext(readFileSync(join(webRoot, 'sw.js'), 'utf8'), sandbox);
  return { handlers, stores, sandbox };
}

async function dispatch(handlers, type, event) {
  const extensions = [];
  event.waitUntil = (p) => void extensions.push(p);
  let responded;
  event.respondWith = (p) => void (responded = Promise.resolve(p));
  handlers[type](event);
  await Promise.all(extensions);
  // Let fire-and-forget cache writes settle.
  await new Promise((r) => setImmediate(r));
  return responded ? { response: await responded } : { response: undefined };
}

const getRequest = (path, mode = 'no-cors') => ({ method: 'GET', url: ORIGIN + path, mode });

// Default network stub: echoes the path. Handles both forms the worker uses —
// addAll(path string) during install and fetch(request) at runtime.
async function installed(
  fetchImpl = async (p) => new Response('shell:' + (typeof p === 'string' ? p : new URL(p.url).pathname)),
) {
  const worker = loadWorker(fetchImpl);
  await dispatch(worker.handlers, 'install', {});
  return worker;
}

test('precached shell matches the files on disk', async () => {
  const { stores } = await installed();
  const cached = [...stores.values()].flatMap((m) => [...m.keys()]).sort();

  const walk = (dir, prefix) =>
    readdirSync(join(webRoot, dir), { withFileTypes: true }).flatMap((e) =>
      e.isDirectory() ? walk(join(dir, e.name), `${prefix}/${e.name}`) : [`${prefix}/${e.name}`],
    );
  const expected = [
    '/',
    '/manifest.json',
    ...walk('icons', '/icons'),
    ...walk('src', '/src').filter((p) => p !== '/src/selftest.html'), // dev-only page
    ...walk('vendor', '/vendor'),
  ].sort();
  assert.deepEqual(cached, expected);
});

test('network-first: serves the fresh response and rewrites the cache', async () => {
  const { handlers, stores } = await installed();
  const { response } = await dispatch(handlers, 'fetch', {
    request: getRequest('/src/app.js'),
  });
  assert.equal(await response.text(), 'shell:/src/app.js');
  assert.equal(await stores.get('mir-shell-v1').get('/src/app.js').clone().text(), 'shell:/src/app.js');
});

test('relay unreachable: falls back to the cached shell', async () => {
  const worker = await installed();
  worker.sandbox.fetch = async () => {
    throw new TypeError('network down');
  };
  const { response } = await dispatch(worker.handlers, 'fetch', {
    request: getRequest('/src/app.js'),
  });
  assert.equal(await response.text(), 'shell:/src/app.js');

  // Navigations serve the SPA entry, like the server does.
  const nav = await dispatch(worker.handlers, 'fetch', {
    request: getRequest('/some/deep/link', 'navigate'),
  });
  assert.equal(await nav.response.text(), 'shell:/');
});

test('relay unreachable and not cached: 503, not a hang', async () => {
  const worker = loadWorker(async () => {
    throw new TypeError('network down');
  });
  const { response } = await dispatch(worker.handlers, 'fetch', {
    request: getRequest('/never-seen'),
  });
  assert.equal(response.status, 503);
});

test('never intercepts signaling, non-GET, or cross-origin requests', async () => {
  const { handlers } = await installed();
  for (const request of [
    getRequest('/attach'),
    getRequest('/agent/signal'),
    getRequest('/pair'),
    getRequest('/turn-credentials'),
    getRequest('/healthz'),
    { method: 'POST', url: ORIGIN + '/anything', mode: 'no-cors' },
    { method: 'GET', url: 'https://evil.example/x', mode: 'no-cors' },
  ]) {
    const { response } = await dispatch(handlers, 'fetch', { request });
    assert.equal(response, undefined, `${request.method} ${request.url} must pass through`);
  }
});

test('boot.js registers the worker', () => {
  const boot = readFileSync(join(webRoot, 'src', 'boot.js'), 'utf8');
  assert.match(boot, /serviceWorker\.register\('\/sw\.js'\)/);
});

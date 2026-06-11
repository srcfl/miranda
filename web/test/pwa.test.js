// PWA installability wiring: manifest.json must stay valid, its icons must
// exist with the declared dimensions, and index.html must reference it.
// (Offline behavior lives in sw.js — see test/sw.test.js.)
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join, dirname } from 'node:path';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const manifest = JSON.parse(readFileSync(join(webRoot, 'manifest.json'), 'utf8'));

function pngSize(path) {
  const buf = readFileSync(path);
  assert.equal(buf.subarray(1, 4).toString('ascii'), 'PNG', `${path} is not a PNG`);
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

test('manifest has the fields installability requires', () => {
  assert.equal(manifest.name, 'Miranda');
  assert.ok(manifest.short_name);
  assert.equal(manifest.display, 'standalone');
  assert.equal(manifest.start_url, '/');
  assert.equal(manifest.scope, '/');
  assert.ok(manifest.icons.some((i) => i.sizes === '192x192'));
  assert.ok(manifest.icons.some((i) => i.sizes === '512x512' && i.purpose === 'any'));
  assert.ok(manifest.icons.some((i) => i.purpose === 'maskable'));
});

test('manifest icons exist and match their declared sizes', () => {
  for (const icon of manifest.icons) {
    const { width, height } = pngSize(join(webRoot, icon.src));
    const [w, h] = icon.sizes.split('x').map(Number);
    assert.equal(width, w, `${icon.src} width`);
    assert.equal(height, h, `${icon.src} height`);
  }
});

test('index.html links the manifest and iOS icon', () => {
  const html = readFileSync(join(webRoot, 'index.html'), 'utf8');
  assert.match(html, /<link rel="manifest" href="\/manifest\.json"/);
  assert.match(html, /<meta name="theme-color" content="#0b0e14"/);
  assert.match(html, /<link rel="apple-touch-icon" href="\/icons\/apple-touch-icon\.png"/);
  pngSize(join(webRoot, 'icons', 'apple-touch-icon.png'));
});

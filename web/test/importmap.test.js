// web/test/importmap.test.js — guards the gap that node tests cannot see: the
// browser resolves bare module specifiers (`@noble/...`) through the importmap in
// index.html, NOT node_modules. A specifier imported by app code but missing from
// the importmap loads fine under `node --test` yet fails at boot in the browser
// ("Failed to resolve module specifier") — a black screen. This test asserts every
// bare specifier used under web/src/ is mapped. (Regression guard for the missing
// @noble/hashes/pbkdf2 mapping that broke the SPA after wallet derivation moved
// into the sign-in path.)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = join(here, '..');

function walk(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(p));
    else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

// A bare specifier does not start with '.' or '/'. Those are the ones the browser
// resolves via the importmap; relative paths resolve as URLs directly.
function bareSpecifiers(src) {
  const out = new Set();
  const re = /(?:^|\W)(?:import|export)[^'"]*?from\s*['"]([^'"]+)['"]/g;
  let m;
  while ((m = re.exec(src)) !== null) {
    const spec = m[1];
    if (!spec.startsWith('.') && !spec.startsWith('/')) out.add(spec);
  }
  return out;
}

test('every bare import under web/src is in the index.html importmap', () => {
  const html = readFileSync(join(webRoot, 'index.html'), 'utf8');
  const mapJSON = html.match(/<script type="importmap"[^>]*>\s*([\s\S]*?)<\/script>/);
  assert.ok(mapJSON, 'index.html must contain an importmap');
  const imports = JSON.parse(mapJSON[1]).imports;
  const mapped = new Set(Object.keys(imports));

  const missing = [];
  for (const file of walk(join(webRoot, 'src'))) {
    for (const spec of bareSpecifiers(readFileSync(file, 'utf8'))) {
      if (!mapped.has(spec)) missing.push(`${spec}  (in ${file.replace(webRoot + '/', '')})`);
    }
  }
  assert.deepEqual(missing, [], `bare specifiers missing from the importmap:\n  ${missing.join('\n  ')}`);
});

test('every importmap target resolves to a vendored file on disk', () => {
  const html = readFileSync(join(webRoot, 'index.html'), 'utf8');
  const imports = JSON.parse(html.match(/<script type="importmap"[^>]*>\s*([\s\S]*?)<\/script>/)[1]).imports;
  for (const [spec, target] of Object.entries(imports)) {
    assert.doesNotThrow(
      () => readFileSync(join(webRoot, target.replace(/^\//, ''))),
      `importmap target ${target} for ${spec} does not exist on disk`,
    );
  }
});

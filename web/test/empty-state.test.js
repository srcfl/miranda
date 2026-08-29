// web/test/empty-state.test.js — the SPA empty state (U3) offers a copy-paste
// install one-liner. This guards it against silently drifting from the real,
// documented command: README.md's "## Install" section is the source of truth
// (install.sh IS that command, fetched over HTTPS), and app.js's INSTALL_CMD
// constant must stay byte-identical to it.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = join(webRoot, '..');

function readmeInstallCommand() {
  const readme = readFileSync(join(repoRoot, 'README.md'), 'utf8');
  const section = readme.match(/## Install\n([\s\S]*?)\n##\s/);
  assert.ok(section, 'README.md must have an "## Install" section');
  const fence = section[1].match(/```bash\n([\s\S]*?)```/);
  assert.ok(fence, 'the Install section must contain a ```bash fenced command');
  return fence[1].trim();
}

function appInstallCommand() {
  const app = readFileSync(join(webRoot, 'src/app.js'), 'utf8');
  const m = app.match(/const INSTALL_CMD = '([^']+)';/);
  assert.ok(m, 'app.js must define an INSTALL_CMD constant');
  return m[1];
}

test('the SPA empty state install command matches README.md exactly', () => {
  assert.equal(appInstallCommand(), readmeInstallCommand());
});

test('the install command is the documented curl-pipe-sh one-liner', () => {
  const cmd = appInstallCommand();
  assert.match(cmd, /^curl -fsSL https:\/\/raw\.githubusercontent\.com\/srcfl\/miranda\/main\/install\.sh \| sh$/);
});

test('app.js guides the user to `mir up`, not the old two-step `mir pair`', () => {
  // U1 (parallel slice) moves pairing inline into `mir up`; the empty-state copy
  // must hold either way, so it must not hard-code the older `mir pair` step.
  const app = readFileSync(join(webRoot, 'src/app.js'), 'utf8');
  const emptyState = app.slice(app.indexOf('function emptyMachinesView'), app.indexOf('function pollForMachine'));
  assert.match(emptyState, /mir up/);
  assert.doesNotMatch(emptyState, /mir pair/);
});

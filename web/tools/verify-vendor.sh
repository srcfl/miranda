#!/usr/bin/env bash
# Regenerate the vendored @noble crypto bundles from the lockfile-pinned packages
# and fail if they differ from what is committed under web/vendor/.
#
# Why: the Go<->JS interop vectors validate the @noble code in node_modules, but
# the browser SPA imports the *vendored* bundles via the importmap in index.html.
# Without this check a vendored crypto bundle could drift from the pinned @noble
# version — accidental staleness, or a malicious edit to web/vendor/*.js — and
# still pass the whole test suite, the interop vectors, and npm audit. This closes
# that gap: it re-bundles each @noble entry point with a pinned esbuild and
# byte-compares the result against the committed file.
#
# Scope: the six @noble crypto bundles only. They reproduce byte-identically.
# xterm/@xterm-addon-fit/jsqr are deliberately NOT gated here — they are UI, not
# crypto, and their bundles carry an esbuild-version-dependent CJS wrapper that is
# not byte-stable across esbuild releases.
#
# Prerequisite: `npm ci` has been run in web/ so node_modules/@noble exists.
set -euo pipefail

# Pin esbuild: the byte-for-byte output is deterministic per esbuild version, so
# this version must match what the committed bundles were built with. Bump it only
# together with a fresh regeneration + commit of web/vendor/*.
ESBUILD_VERSION="0.28.1"

ROOT="$(cd "$(dirname "$0")/.." && pwd)" # web/
cd "$ROOT"

names=(noble-curves-ed25519 noble-ciphers-chacha noble-hashes-sha2 noble-hashes-hmac noble-hashes-hkdf noble-hashes-utils)
specs=("@noble/curves/ed25519" "@noble/ciphers/chacha" "@noble/hashes/sha2" "@noble/hashes/hmac" "@noble/hashes/hkdf" "@noble/hashes/utils")

TMP="$(mktemp -d)"
ENTRY="$ROOT/.vendor-verify-entry.js" # must live under web/ so esbuild resolves @noble from web/node_modules
trap 'rm -rf "$TMP" "$ENTRY"' EXIT

fail=0
for i in "${!names[@]}"; do
  name="${names[$i]}"
  spec="${specs[$i]}"
  printf "export * from '%s';\n" "$spec" >"$ENTRY"
  npx --yes "esbuild@${ESBUILD_VERSION}" "$ENTRY" \
    --bundle --minify --format=esm --outfile="$TMP/$name.js" >/dev/null 2>&1
  if ! cmp -s "$TMP/$name.js" "vendor/$name.js"; then
    echo "VENDOR DRIFT: vendor/$name.js does not match a fresh esbuild of $spec"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "One or more vendored @noble crypto bundles differ from the pinned packages."
  echo "Regenerate with esbuild@${ESBUILD_VERSION} (--bundle --minify --format=esm) and commit,"
  echo "or investigate why web/vendor/ was edited out of band."
  exit 1
fi
echo "vendored @noble crypto bundles match the lockfile-pinned packages"

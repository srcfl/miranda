#!/usr/bin/env bash
# Build each release binary twice in independent build caches and compare bytes.
# This proves same-source reproducibility for the current OS/architecture. Run it
# on every target platform when independently verifying a published release.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/miranda-repro.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty)}"
VERSION="${VERSION#v}"
COMMIT="$(git -C "$ROOT" rev-parse --short=8 HEAD)"
DATE="$(git -C "$ROOT" show -s --format=%cI HEAD)"
export SOURCE_DATE_EPOCH="$(git -C "$ROOT" show -s --format=%ct HEAD)"

build_set() {
  local output="$1" cache="$2"
  mkdir -p "$output" "$cache"
  while read -r binary package; do
    (
      cd "$ROOT/go"
      CGO_ENABLED=0 GOCACHE="$cache" go build \
        -trimpath -buildvcs=false \
        -ldflags "-s -w -buildid= -X github.com/srcful/terminal-relay/go/internal/version.Version=$VERSION -X github.com/srcful/terminal-relay/go/internal/version.Commit=$COMMIT -X github.com/srcful/terminal-relay/go/internal/version.Date=$DATE" \
        -o "$output/$binary" "$package"
    )
  done <<'EOF'
mir ./cmd/mir
mir-agent ./cmd/mir-agent
mir-signal ./cmd/mir-signal
EOF
}

build_set "$TMP/one" "$TMP/cache-one"
build_set "$TMP/two" "$TMP/cache-two"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

for binary in mir mir-agent mir-signal; do
  if ! cmp -s "$TMP/one/$binary" "$TMP/two/$binary"; then
    echo "FAIL: $binary differs between clean build caches" >&2
    exit 1
  fi
  echo "$(hash_file "$TMP/one/$binary")  $binary"
done
echo "reproducible: all binaries are byte-identical for $(go env GOOS)/$(go env GOARCH)"

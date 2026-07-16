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
# These -X values are baked into the binary, so to reproduce a *published*
# release the recipe must derive them EXACTLY as GoReleaser does (.goreleaser.yaml):
#   .ShortCommit -> first 7 chars of the full SHA (not `--short=8`, and not git's
#                   variable-length abbreviation).
#   .CommitDate  -> the commit time in UTC RFC3339 with a literal Z (not `%cI`,
#                   which emits the local-timezone offset and made the hash both
#                   wrong vs. the release AND dependent on the reviewer's timezone).
# With the old 8-char commit / local-tz date, this script could only ever prove
# self-consistency (build twice locally); it never matched an honest release.
COMMIT="$(git -C "$ROOT" rev-parse HEAD | cut -c1-7)"
DATE="$(TZ=UTC0 git -C "$ROOT" show -s --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd HEAD)"

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

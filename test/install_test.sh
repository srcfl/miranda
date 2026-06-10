#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
MIR_INSTALL_LIB=1 . "$here/../install.sh"

fail() { echo "FAIL: $1" >&2; exit 1; }

# detect_os_arch emits "<os>/<arch>" using uname; just assert it is non-empty and slash-formed.
got=$(detect_os_arch) || fail "detect_os_arch errored"
case "$got" in */*) : ;; *) fail "detect_os_arch=$got not os/arch" ;; esac

# verify_sha256: build a file, hash it, assert pass on match and fail on mismatch.
tmp=$(mktemp); printf 'hello' > "$tmp"
sum=$(sha256_of "$tmp")
printf '%s  payload.tar.gz\n' "$sum" > "$tmp.sums"
verify_sha256 "$tmp" "payload.tar.gz" "$tmp.sums" || fail "verify_sha256 rejected a matching checksum"
printf 'deadbeef  payload.tar.gz\n' > "$tmp.bad"
if verify_sha256 "$tmp" "payload.tar.gz" "$tmp.bad"; then fail "verify_sha256 accepted a bad checksum"; fi

echo "OK install_test"

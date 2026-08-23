#!/bin/sh
# Fail closed unless a web/SPA tarball matches a required digest.
# Usage:
#   verify-web-integrity.sh <tarball> <expected-sha256>
#   MIR_WEB_SHA256=<sha> verify-web-integrity.sh <tarball>
#   verify-web-integrity.sh --require-env
# Missing digest, mismatch, or a missing tarball is non-zero.
set -eu

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	else shasum -a 256 "$1" | awk '{print $1}'; fi
}

if [ "${1:-}" = "--require-env" ]; then
	if [ -z "${MIR_WEB_SHA256:-}" ]; then
		echo "FATAL: MIR_WEB_SHA256 is required — refusing unsigned SPA" >&2
		exit 1
	fi
	exit 0
fi

TARBALL="${1:-}"
EXPECT="${2:-${MIR_WEB_SHA256:-}}"
if [ -z "$TARBALL" ] || [ ! -f "$TARBALL" ]; then
	echo "FATAL: SPA tarball missing" >&2
	exit 1
fi
if [ -z "$EXPECT" ]; then
	echo "FATAL: no expected SPA digest (pass arg or MIR_WEB_SHA256)" >&2
	exit 1
fi
GOT="$(sha256_of "$TARBALL")"
if [ "$GOT" != "$EXPECT" ]; then
	echo "FATAL: SPA digest mismatch" >&2
	echo "       expected $EXPECT" >&2
	echo "       got      $GOT" >&2
	exit 1
fi
echo "spa integrity ok $GOT"

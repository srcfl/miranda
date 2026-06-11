#!/bin/sh
# Miranda installer. Usage:
#   curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh
#   ...| sh -s -- --agent     # install mir-agent instead of mir
#   ...| sh -s -- --all       # install both
# Env: MIR_VERSION=v0.1.0 (pin), INSTALL_DIR=/usr/local/bin (override target).
set -eu

REPO="srcfl/miranda"
WHICH="mir" # mir | agent | all

# Cosign keyless trust anchors. checksums.txt is signed in CI by the release
# workflow using its GitHub Actions OIDC identity (no stored key). These two
# values pin WHO is allowed to have signed it:
#   - the cert's SAN must match the release workflow on a version tag, and
#   - the cert must have been issued off a GitHub Actions OIDC token.
# A valid signature therefore proves checksums.txt came from THIS repo's release
# pipeline — see verify_checksums_sig() for the full trust-model note.
COSIGN_IDENTITY_RE="https://github.com/$REPO/.github/workflows/release.yml@refs/tags/v.*"
COSIGN_OIDC_ISSUER="https://token.actions.githubusercontent.com"

# --- pure helpers (also used by test/install_test.sh via MIR_INSTALL_LIB) ---

detect_os_arch() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	arch=$(uname -m)
	case "$os" in linux) os=linux ;; darwin) os=darwin ;; *) echo "unsupported OS: $os" >&2; return 1 ;; esac
	case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported arch: $arch" >&2; return 1 ;; esac
	printf '%s/%s' "$os" "$arch"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# verify_sha256 <file> <name-in-checksums> <checksums-file> -> 0 if match
verify_sha256() {
	_want=$(awk -v n="$2" '$2==n {print $1}' "$3")
	[ -n "$_want" ] || { echo "no checksum for $2" >&2; return 1; }
	_got=$(sha256_of "$1")
	[ "$_want" = "$_got" ]
}

# verify_checksums_sig <base-url> <tmp-dir> -> 0 if verified OR cosign unavailable.
#
# TRUST MODEL. The per-archive SHA256 in checksums.txt is only as trustworthy as
# checksums.txt itself. Without a signature, anyone who can publish a GitHub
# Release could replace BOTH an archive and its checksum and this installer would
# happily "verify" the swap. checksums.txt.sig + checksums.txt.pem are a cosign
# keyless signature: `cosign verify-blob` here re-checks, against Sigstore's
# trust roots and the Rekor transparency log, that checksums.txt was signed by a
# Fulcio cert whose identity ($COSIGN_IDENTITY_RE) is this repo's release
# workflow on a version tag, issued from a GitHub Actions OIDC token
# ($COSIGN_OIDC_ISSUER). A pass means: checksums.txt genuinely came from THIS
# repo's release pipeline. It does NOT vouch for the source code that was built —
# only for the provenance of the checksum file.
#
# Graceful degradation: cosign is optional tooling. If it is not installed we
# print one warning and fall back to checksum-only verification (still protects
# against corrupted downloads / CDN tampering, just not a malicious publisher).
# If cosign IS present and verification FAILS, that is an active attack signal —
# abort loudly.
verify_checksums_sig() {
	_base="$1"; _tmp="$2"
	if ! command -v cosign >/dev/null 2>&1; then
		echo "warning: cosign not found; skipping signature check of checksums.txt (install cosign for supply-chain verification) — falling back to checksum-only" >&2
		return 0
	fi
	# Both signing artifacts must be present to verify; treat a missing one as a
	# hard failure (an attacker stripping the .sig must not silently downgrade us).
	if ! curl -fsSL "$_base/checksums.txt.sig" -o "$_tmp/checksums.txt.sig" \
		|| ! curl -fsSL "$_base/checksums.txt.pem" -o "$_tmp/checksums.txt.pem"; then
		echo "cosign present but checksums.txt signature/cert assets are missing from the release; refusing to proceed" >&2
		return 1
	fi
	if cosign verify-blob \
		--certificate "$_tmp/checksums.txt.pem" \
		--signature "$_tmp/checksums.txt.sig" \
		--certificate-identity-regexp "$COSIGN_IDENTITY_RE" \
		--certificate-oidc-issuer "$COSIGN_OIDC_ISSUER" \
		"$_tmp/checksums.txt" >/dev/null 2>&1; then
		echo "cosign: checksums.txt signature verified (provenance: $REPO release workflow)"
		return 0
	fi
	echo "cosign: SIGNATURE VERIFICATION FAILED for checksums.txt — aborting (possible tampering)" >&2
	return 1
}

latest_tag() {
	curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
		| awk -F'"' '/"tag_name"/ {print $4; exit}'
}

# --- everything below only runs for a real install ---
if [ "${MIR_INSTALL_LIB:-}" = "1" ]; then return 0 2>/dev/null || exit 0; fi

while [ $# -gt 0 ]; do
	case "$1" in
		--agent) WHICH=agent ;;
		--all) WHICH=all ;;
		mir|--mir) WHICH=mir ;;
		*) echo "unknown arg: $1" >&2; exit 2 ;;
	esac
	shift
done

osarch=$(detect_os_arch); os=${osarch%/*}; arch=${osarch#*/}
tag=${MIR_VERSION:-$(latest_tag)}
[ -n "$tag" ] || { echo "could not resolve latest release tag" >&2; exit 1; }
ver=${tag#v}
dir=${INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$dir"

case "$WHICH" in mir) bins="mir" ;; agent) bins="mir-agent" ;; all) bins="mir mir-agent" ;; esac

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
base="https://github.com/$REPO/releases/download/$tag"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
# Verify the checksum file's signature before trusting any digest inside it.
verify_checksums_sig "$base" "$tmp" || exit 1

for bin in $bins; do
	archive="${bin}_${ver}_${os}_${arch}.tar.gz"
	echo "downloading $archive ..."
	curl -fsSL "$base/$archive" -o "$tmp/$archive"
	verify_sha256 "$tmp/$archive" "$archive" "$tmp/checksums.txt" || { echo "checksum mismatch for $archive" >&2; exit 1; }
	tar -xzf "$tmp/$archive" -C "$tmp"
	install -m 0755 "$tmp/$bin" "$dir/$bin"
	echo "installed $bin -> $dir/$bin"
done

case ":$PATH:" in
	*":$dir:"*) : ;;
	*) echo; echo "note: $dir is not on your PATH. Add it:"; echo "  export PATH=\"$dir:\$PATH\"" ;;
esac

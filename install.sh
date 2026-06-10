#!/bin/sh
# Miranda installer. Usage:
#   curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh
#   ...| sh -s -- --agent     # install mir-agent instead of mir
#   ...| sh -s -- --all       # install both
# Env: MIR_VERSION=v0.1.0 (pin), INSTALL_DIR=/usr/local/bin (override target).
set -eu

REPO="srcfl/miranda"
WHICH="mir" # mir | agent | all

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

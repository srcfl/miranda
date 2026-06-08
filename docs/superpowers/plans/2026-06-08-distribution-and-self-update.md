# Distribution & Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a `curl | sh` installer and a pull-based, checksum-verified self-update for `mir` and `mir-agent`, fed by a tagged GoReleaser pipeline.

**Architecture:** A pushed semver tag triggers GitHub Actions → GoReleaser, which builds all three binaries for `darwin/linux × arm64/amd64` and publishes a GitHub Release with per-binary archives and one `checksums.txt`. A shared `internal/version` carries the ldflags-stamped version; a shared `internal/selfupdate` resolves the latest release, verifies SHA256 over HTTPS, and atomically swaps the running binary. Self-update is notify-by-default, manual via a `self-update` subcommand, and auto only when explicitly opted in.

**Tech Stack:** Go 1.26, GoReleaser, GitHub Actions, POSIX `sh`, `golang.org/x/mod/semver`, GitHub Releases REST API.

**Reference spec:** `docs/superpowers/specs/2026-06-08-distribution-and-self-update-design.md`

---

## Conventions locked here (every later task depends on these)

- **Repo slug:** `srcfl/miranda`.
- **Release tag format:** `vMAJOR.MINOR.PATCH` (e.g. `v0.1.0`). GoReleaser strips the leading `v`, so `.Version` inside archive names is `0.1.0`.
- **Archive name template (per binary):** `{{ .Binary }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}.tar.gz` → e.g. `mir_0.1.0_darwin_arm64.tar.gz`, `mir-agent_0.1.0_linux_amd64.tar.gz`.
- **Checksums file:** a single `checksums.txt` (SHA256, one `<hex>  <filename>` line per archive).
- **OS/arch tokens:** GoReleaser uses raw `GOOS`/`GOARCH` (`darwin`, `linux`, `arm64`, `amd64`), which match Go's `runtime.GOOS`/`runtime.GOARCH` at update time.
- **Env var prefix:** `MIR_` (matches `internal/defaults`).
- **Config dir:** `~/.terminal-relay` (matches `defaultDir()` in both cmds). The update-check cache lives there.

---

## File Structure

**Create:**
- `go/internal/version/version.go` — embedded version string + `--version` helper.
- `go/internal/selfupdate/selfupdate.go` — release lookup, asset resolution, semver compare.
- `go/internal/selfupdate/verify.go` — `checksums.txt` parsing + SHA256 verification.
- `go/internal/selfupdate/apply.go` — download, atomic swap, re-exec.
- `go/internal/selfupdate/notice.go` — throttled background check + cached "update available" notice.
- `go/internal/selfupdate/*_test.go` — unit tests with a stubbed HTTP server.
- `.goreleaser.yaml` — build matrix + archives + checksums.
- `.github/workflows/release.yml` — tag-triggered release job.
- `install.sh` — repo-root installer.
- `test/install_test.sh` — POSIX shell test for the installer.

**Modify:**
- `go/cmd/mir/main.go` — `--version`, `self-update` subcommand, startup notice hook.
- `go/cmd/mir-agent/main.go` — `--version`, `self-update` subcommand, `--auto-update` flag, startup notice hook.
- `go/cmd/mir-signal/main.go` — `--version` only (no installer / self-update).
- `go/internal/agent/runtime.go` — active-session counter + `ActiveSessions()` accessor (for auto-update idle gating).
- `go/go.mod` / `go/go.sum` — add `golang.org/x/mod`.
- `README.md` — install + update instructions.

---

# Phase A — Versioning & Release Pipeline

## Task 1: `internal/version` package + `--version` on all three CLIs

**Files:**
- Create: `go/internal/version/version.go`
- Create: `go/internal/version/version_test.go`
- Modify: `go/cmd/mir/main.go`, `go/cmd/mir-agent/main.go`, `go/cmd/mir-signal/main.go`

- [ ] **Step 1: Write the failing test**

`go/internal/version/version_test.go`:
```go
package version

import (
	"strings"
	"testing"
)

func TestStringIncludesVersionCommitDate(t *testing.T) {
	Version, Commit, Date = "1.2.3", "abc1234", "2026-06-08T00:00:00Z"
	got := String()
	for _, want := range []string{"1.2.3", "abc1234", "2026-06-08"} {
		if !strings.Contains(got, want) {
			t.Fatalf("String()=%q missing %q", got, want)
		}
	}
}

func TestStringDefaultsToDev(t *testing.T) {
	Version, Commit, Date = "dev", "none", "unknown"
	if got := String(); !strings.Contains(got, "dev") {
		t.Fatalf("String()=%q, want it to contain \"dev\"", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/version/ -run TestString -v`
Expected: FAIL — `undefined: String` / `undefined: Version`.

- [ ] **Step 3: Write minimal implementation**

`go/internal/version/version.go`:
```go
// Package version holds the build-time version stamped via -ldflags. GoReleaser
// overrides these at release; a plain `go build` leaves the dev defaults.
package version

import "fmt"

var (
	Version = "dev"     // set via -ldflags -X .../version.Version=...
	Commit  = "none"    // short git sha
	Date    = "unknown" // RFC3339 build date
)

// String renders a one-line human version, e.g. "1.2.3 (abc1234, 2026-06-08)".
func String() string {
	date := Date
	if len(date) >= 10 {
		date = date[:10] // trim RFC3339 to YYYY-MM-DD
	}
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, date)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/version/ -v`
Expected: PASS.

- [ ] **Step 5: Wire `--version` into each cmd's dispatch**

In `go/cmd/mir/main.go`, add the import `"github.com/srcful/terminal-relay/go/internal/version"` and a case at the top of the `switch os.Args[1]` block in `main()`:
```go
	case "--version", "-v", "version":
		fmt.Println("mir", version.String())
		return
```
In `go/cmd/mir-agent/main.go`, same import, same case but printing `"mir-agent"`.
In `go/cmd/mir-signal/main.go`, add the import and handle `--version` before its normal flag parsing, printing `"mir-signal"`. (mir-signal uses `flag` at top of `main`; add an early `if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") { fmt.Println("mir-signal", version.String()); return }`.)

- [ ] **Step 6: Verify the ldflags wiring end-to-end**

Run:
```bash
cd go && go run -ldflags "-X github.com/srcful/terminal-relay/go/internal/version.Version=9.9.9" ./cmd/mir --version
```
Expected output contains: `mir 9.9.9 (none, unknown)`.

- [ ] **Step 7: Commit**

```bash
git add go/internal/version go/cmd/mir/main.go go/cmd/mir-agent/main.go go/cmd/mir-signal/main.go
git commit -m "feat(version): internal/version package + --version on all CLIs"
```

---

## Task 2: GoReleaser config + GitHub Actions release workflow

**Files:**
- Create: `.goreleaser.yaml`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Write `.goreleaser.yaml`**

```yaml
version: 2
project_name: miranda

before:
  hooks:
    - sh -c "cd go && go mod tidy"

builds:
  - id: mir
    main: ./cmd/mir
    binary: mir
    dir: go
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/srcful/terminal-relay/go/internal/version.Version={{ .Version }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Commit={{ .ShortCommit }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Date={{ .Date }}
  - id: mir-agent
    main: ./cmd/mir-agent
    binary: mir-agent
    dir: go
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/srcful/terminal-relay/go/internal/version.Version={{ .Version }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Commit={{ .ShortCommit }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Date={{ .Date }}
  - id: mir-signal
    main: ./cmd/mir-signal
    binary: mir-signal
    dir: go
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/srcful/terminal-relay/go/internal/version.Version={{ .Version }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Commit={{ .ShortCommit }}
      - -X github.com/srcful/terminal-relay/go/internal/version.Date={{ .Date }}

archives:
  # One archive entry PER build id. A single entry covering multiple builds would
  # bundle all binaries into one archive per platform and make {{ .Binary }}
  # ambiguous — the installer/self-update need one archive per binary.
  - id: mir
    builds: [mir]
    name_template: "{{ .Binary }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    format: tar.gz
    files: [] # binary only; no README/LICENSE noise inside the archive
  - id: mir-agent
    builds: [mir-agent]
    name_template: "{{ .Binary }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    format: tar.gz
    files: []
  - id: mir-signal
    builds: [mir-signal]
    name_template: "{{ .Binary }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    format: tar.gz
    files: []

checksum:
  name_template: "checksums.txt"
  algorithm: sha256

release:
  github:
    owner: srcfl
    name: miranda
  draft: false
  prerelease: auto

changelog:
  use: github
```

- [ ] **Step 2: Validate the config locally**

Run: `goreleaser check`
Expected: `1 configuration file(s) validated` with no errors. (Install GoReleaser first if missing: `brew install goreleaser`.)

- [ ] **Step 3: Dry-run a snapshot build (no publish)**

Run: `goreleaser release --snapshot --clean --skip=publish`
Expected: `dist/` contains `mir_*_darwin_arm64.tar.gz`, `mir-agent_*`, `mir-signal_*` for all four os/arch combos, plus `checksums.txt`. Confirm:
```bash
ls dist/*.tar.gz | wc -l   # expect 12 (3 binaries × 4 platforms)
cat dist/checksums.txt | wc -l  # expect 12
```

- [ ] **Step 4: Verify the snapshot binary reports its version**

Run:
```bash
tar -xzf dist/mir_*_$(go env GOOS)_$(go env GOARCH).tar.gz -O mir > /tmp/mir-snap && chmod +x /tmp/mir-snap && /tmp/mir-snap --version
```
Expected: a non-`dev` version string stamped from the snapshot.

- [ ] **Step 5: Write the GitHub Actions release workflow**

`.github/workflows/release.yml`:
```yaml
name: release
on:
  push:
    tags: ["v*"]

permissions:
  contents: write # required to create the GitHub Release

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # full history for changelog
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26.x"
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 6: Commit**

```bash
git add .goreleaser.yaml .github/workflows/release.yml
git commit -m "ci: GoReleaser pipeline + tag-triggered release workflow"
```

> **Note:** the first real release happens when someone pushes a tag (`git tag v0.1.0 && git push origin v0.1.0`). Do NOT push a tag as part of implementation — that is a release decision for the user.

---

# Phase B — Installer

## Task 3: `install.sh` + shell test

**Files:**
- Create: `install.sh`
- Create: `test/install_test.sh`

- [ ] **Step 1: Write the failing shell test**

`test/install_test.sh` exercises the two pure functions the installer exposes when sourced with `MIR_INSTALL_LIB=1` (so sourcing does not trigger a real install): `detect_os_arch` and `verify_sha256`.
```sh
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `sh test/install_test.sh`
Expected: FAIL — `install.sh` does not exist yet / functions undefined.

- [ ] **Step 3: Write `install.sh`**

```sh
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `sh test/install_test.sh`
Expected: `OK install_test`.

- [ ] **Step 5: Lint the script**

Run: `shellcheck install.sh test/install_test.sh` (if available)
Expected: no errors. (Warnings about `read`-without-`-r` etc. acceptable if none present.)

- [ ] **Step 6: Commit**

```bash
git add install.sh test/install_test.sh
git commit -m "feat(install): curl|sh installer with checksum verification + shell test"
```

---

# Phase C — Self-Update

## Task 4: `internal/selfupdate` — release lookup, asset resolution, semver compare

**Files:**
- Create: `go/internal/selfupdate/selfupdate.go`
- Create: `go/internal/selfupdate/selfupdate_test.go`
- Modify: `go/go.mod`, `go/go.sum`

- [ ] **Step 1: Add the semver dependency**

Run: `cd go && go get golang.org/x/mod/semver`
Expected: `go.mod` now requires `golang.org/x/mod`.

- [ ] **Step 2: Write the failing test**

`go/internal/selfupdate/selfupdate_test.go`:
```go
package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeAPI(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/srcfl/miranda/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestLatestParsesTagAndAsset(t *testing.T) {
	srv := fakeAPI(t, `{
		"tag_name": "v0.2.0",
		"assets": [
			{"name": "mir_0.2.0_linux_amd64.tar.gz", "browser_download_url": "http://x/mir.tgz"},
			{"name": "checksums.txt", "browser_download_url": "http://x/checksums.txt"}
		]
	}`)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}
	rel, err := c.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.2.0" {
		t.Fatalf("tag=%q", rel.Tag)
	}
	if rel.AssetURL == "" || rel.ChecksumsURL == "" {
		t.Fatalf("asset=%q checksums=%q", rel.AssetURL, rel.ChecksumsURL)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"dev", "v0.2.0", true}, // dev always treated as older
	}
	for _, tc := range cases {
		if got := IsNewer(tc.cur, tc.latest); got != tc.want {
			t.Fatalf("IsNewer(%q,%q)=%v want %v", tc.cur, tc.latest, got, tc.want)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd go && go test ./internal/selfupdate/ -v`
Expected: FAIL — `Client` / `Latest` / `IsNewer` undefined.

- [ ] **Step 4: Write the implementation**

`go/internal/selfupdate/selfupdate.go`:
```go
// Package selfupdate resolves the latest GitHub Release for a binary and
// (in apply.go) swaps the running executable after SHA256 verification.
// It talks to GitHub directly — never through the relay.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Client describes one binary's update channel. Fields default via New().
type Client struct {
	APIBase string // e.g. https://api.github.com (override in tests)
	Repo    string // "srcfl/miranda"
	Binary  string // "mir" | "mir-agent"
	OS      string // runtime.GOOS
	Arch    string // runtime.GOARCH
	HTTP    *http.Client
}

// New builds a Client for the current platform with sane defaults.
func New(repo, binary string) *Client {
	return &Client{
		APIBase: "https://api.github.com",
		Repo:    repo,
		Binary:  binary,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Release is the resolved latest release for this Client's platform.
type Release struct {
	Tag          string
	AssetURL     string // archive for this binary/os/arch
	AssetName    string // archive filename (matched against checksums.txt)
	ChecksumsURL string
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName is the archive filename GoReleaser produces for this platform.
func (c *Client) assetName(tag string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", c.Binary, strings.TrimPrefix(tag, "v"), c.OS, c.Arch)
}

// Latest fetches and resolves the most recent release.
func (c *Client) Latest() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBase, "/"), c.Repo)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases: %s", resp.Status)
	}
	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, err
	}
	want := c.assetName(gr.TagName)
	rel := &Release{Tag: gr.TagName, AssetName: want}
	for _, a := range gr.Assets {
		switch a.Name {
		case want:
			rel.AssetURL = a.URL
		case "checksums.txt":
			rel.ChecksumsURL = a.URL
		}
	}
	if rel.AssetURL == "" {
		return nil, fmt.Errorf("no asset %q in release %s", want, gr.TagName)
	}
	if rel.ChecksumsURL == "" {
		return nil, fmt.Errorf("no checksums.txt in release %s", gr.TagName)
	}
	return rel, nil
}

// IsNewer reports whether latest (a tag, with or without leading v) is a higher
// semver than cur. A non-semver cur (e.g. "dev") is always treated as older.
func IsNewer(cur, latest string) bool {
	c := canon(cur)
	l := canon(latest)
	if !semver.IsValid(c) {
		return true
	}
	if !semver.IsValid(l) {
		return false
	}
	return semver.Compare(l, c) > 0
}

func canon(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./internal/selfupdate/ -v`
Expected: PASS (`TestLatestParsesTagAndAsset`, `TestIsNewer`).

- [ ] **Step 6: Commit**

```bash
git add go/internal/selfupdate/selfupdate.go go/internal/selfupdate/selfupdate_test.go go/go.mod go/go.sum
git commit -m "feat(selfupdate): release lookup, asset resolution, semver compare"
```

---

## Task 5: Checksum verification + download/atomic-swap apply

**Files:**
- Create: `go/internal/selfupdate/verify.go`
- Create: `go/internal/selfupdate/apply.go`
- Create: `go/internal/selfupdate/apply_test.go`

- [ ] **Step 1: Write the failing test**

`go/internal/selfupdate/apply_test.go`:
```go
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// makeArchive returns a tar.gz containing one entry named `bin` with `payload`.
func makeArchive(t *testing.T, bin string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: bin, Mode: 0o755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("new-binary-bytes")
	sum := sha256.Sum256(data)
	line := fmt.Sprintf("%s  mir_1.0.0_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))
	if err := verifyChecksum(data, "mir_1.0.0_linux_amd64.tar.gz", []byte(line)); err != nil {
		t.Fatalf("good checksum rejected: %v", err)
	}
	bad := "deadbeef  mir_1.0.0_linux_amd64.tar.gz\n"
	if err := verifyChecksum(data, "mir_1.0.0_linux_amd64.tar.gz", []byte(bad)); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestApplyReplacesTarget(t *testing.T) {
	payload := []byte("#!/bin/sh\necho v2\n")
	archive := makeArchive(t, "mir", payload)
	sum := sha256.Sum256(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			_, _ = w.Write(archive)
		case "/checksums":
			fmt.Fprintf(w, "%s  mir_1.0.0_%s_%s.tar.gz\n", hex.EncodeToString(sum[:]), "os", "arch")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "mir")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Binary: "mir", OS: "os", Arch: "arch", HTTP: srv.Client()}
	rel := &Release{Tag: "v1.0.0", AssetName: "mir_1.0.0_os_arch.tar.gz", AssetURL: srv.URL + "/asset", ChecksumsURL: srv.URL + "/checksums"}
	if err := c.Apply(rel, target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, payload) {
		t.Fatalf("target not replaced: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/selfupdate/ -run 'TestVerify|TestApply' -v`
Expected: FAIL — `verifyChecksum` / `Apply` undefined.

- [ ] **Step 3: Write `verify.go`**

```go
package selfupdate

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// verifyChecksum confirms data's SHA256 matches the entry for name in a
// GoReleaser-style checksums.txt ("<hex>  <name>" per line).
func verifyChecksum(data []byte, name string, checksums []byte) error {
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := bytes.Fields(sc.Bytes())
		if len(fields) == 2 && string(fields[1]) == name {
			want = string(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum entry for %s", name)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: have %s want %s", name, got, want)
	}
	return nil
}
```

- [ ] **Step 4: Write `apply.go`**

```go
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// Apply downloads the release archive, verifies its checksum, extracts the
// binary, and atomically replaces targetPath. targetPath should be the absolute
// path of the currently running executable (see os.Executable).
func (c *Client) Apply(rel *Release, targetPath string) error {
	archive, err := c.fetch(rel.AssetURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	sums, err := c.fetch(rel.ChecksumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyChecksum(archive, rel.AssetName, sums); err != nil {
		return err
	}
	binData, err := extractBinary(archive, c.Binary)
	if err != nil {
		return err
	}
	return swap(targetPath, binData)
}

func (c *Client) fetch(url string) ([]byte, error) {
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// extractBinary pulls the entry named `name` out of a tar.gz.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// swap writes data to a temp file in target's directory then atomically renames
// it over target. Safe on Unix even while target is the running process.
func swap(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".mir-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// ReExec replaces the current process image with the binary at path, preserving
// PID/FDs (so a systemd/supervisor wrapper survives). Unix only.
func ReExec(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd go && go test ./internal/selfupdate/ -v`
Expected: PASS (verify + apply + earlier tests).

- [ ] **Step 6: Commit**

```bash
git add go/internal/selfupdate/verify.go go/internal/selfupdate/apply.go go/internal/selfupdate/apply_test.go
git commit -m "feat(selfupdate): SHA256 verify + atomic swap + re-exec"
```

---

## Task 6: Throttled update-check + "update available" notice (Layer 1)

**Files:**
- Create: `go/internal/selfupdate/notice.go`
- Create: `go/internal/selfupdate/notice_test.go`

- [ ] **Step 1: Write the failing test**

`go/internal/selfupdate/notice_test.go`:
```go
package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldCheckThrottle(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update-check.json")
	// No file yet -> should check.
	if !shouldCheck(cache, time.Hour) {
		t.Fatal("expected check when no cache exists")
	}
	// Record a check "now"; within the window -> should not check.
	if err := writeCheck(cache, "v0.2.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if shouldCheck(cache, time.Hour) {
		t.Fatal("expected no check within throttle window")
	}
	// Backdate it past the window -> should check again.
	if err := writeCheck(cache, "v0.2.0", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !shouldCheck(cache, time.Hour) {
		t.Fatal("expected check after throttle window elapsed")
	}
}

func TestCachedLatest(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update-check.json")
	_ = writeCheck(cache, "v0.9.0", time.Now())
	if got := cachedLatest(cache); got != "v0.9.0" {
		t.Fatalf("cachedLatest=%q", got)
	}
	if got := cachedLatest(filepath.Join(t.TempDir(), "missing.json")); got != "" {
		t.Fatalf("expected empty for missing cache, got %q", got)
	}
	_ = os.Remove(cache)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/selfupdate/ -run 'TestShouldCheck|TestCachedLatest' -v`
Expected: FAIL — `shouldCheck` / `writeCheck` / `cachedLatest` undefined.

- [ ] **Step 3: Write `notice.go`**

```go
package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type checkState struct {
	LatestTag string    `json:"latest_tag"`
	CheckedAt time.Time `json:"checked_at"`
}

func shouldCheck(cachePath string, window time.Duration) bool {
	st, err := readCheck(cachePath)
	if err != nil {
		return true // no/invalid cache => check
	}
	return time.Since(st.CheckedAt) >= window
}

func readCheck(cachePath string) (*checkState, error) {
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st checkState
	if err := json.NewDecoder(f).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeCheck(cachePath, latestTag string, at time.Time) error {
	b, err := json.Marshal(checkState{LatestTag: latestTag, CheckedAt: at})
	if err != nil {
		return err
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath)
}

func cachedLatest(cachePath string) string {
	st, err := readCheck(cachePath)
	if err != nil {
		return ""
	}
	return st.LatestTag
}

// MaybeNotify runs at most once per `window`: if a newer release exists it
// writes a one-line notice to w (stderr) and caches the result. All failures are
// silent — this must never disrupt normal command output. currentVersion is the
// running binary's version.String()-style tag (e.g. "0.1.0" or "dev").
func (c *Client) MaybeNotify(w io.Writer, cachePath, currentVersion string, window time.Duration) {
	if os.Getenv("MIR_NO_UPDATE_CHECK") == "1" {
		return
	}
	if !shouldCheck(cachePath, window) {
		// still surface a cached newer version, but do not hit the network
		if tag := cachedLatest(cachePath); tag != "" && IsNewer(currentVersion, tag) {
			fmt.Fprintf(w, "Update available: %s → %s   run: %s self-update\n", currentVersion, tag, c.Binary)
		}
		return
	}
	rel, err := c.Latest()
	if err != nil {
		return // silent
	}
	_ = writeCheck(cachePath, rel.Tag, time.Now())
	if IsNewer(currentVersion, rel.Tag) {
		fmt.Fprintf(w, "Update available: %s → %s   run: %s self-update\n", currentVersion, rel.Tag, c.Binary)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/selfupdate/ -v`
Expected: PASS (all selfupdate tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/selfupdate/notice.go go/internal/selfupdate/notice_test.go
git commit -m "feat(selfupdate): throttled update-check cache + stderr notice"
```

---

## Task 7: `self-update` subcommand + startup notice on `mir` and `mir-agent`

**Files:**
- Modify: `go/cmd/mir/main.go`
- Modify: `go/cmd/mir-agent/main.go`

- [ ] **Step 1: Add a shared repo constant + cache path helper to each cmd**

In `go/cmd/mir/main.go`, add near the top (after `defaultDir`):
```go
const repoSlug = "srcfl/miranda"

func updateCachePath(dir string) string {
	return filepath.Join(dir, "update-check.json")
}
```
Add the same two declarations to `go/cmd/mir-agent/main.go`. (Both files already import `path/filepath`.)

- [ ] **Step 2: Add the `self-update` subcommand to `mir`**

In `go/cmd/mir/main.go`, add `"github.com/srcful/terminal-relay/go/internal/selfupdate"` and `"github.com/srcful/terminal-relay/go/internal/version"` to imports, a dispatch case, and the handler:
```go
	case "self-update":
		cmdSelfUpdate(os.Args[2:])
```
```go
func cmdSelfUpdate(args []string) {
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := selfupdate.New(repoSlug, "mir")
	rel, err := c.Latest()
	if err != nil {
		fatal(err)
	}
	if !selfupdate.IsNewer(version.Version, rel.Tag) {
		fmt.Printf("already up to date (%s)\n", version.Version)
		return
	}
	fmt.Printf("updating mir %s → %s …\n", version.Version, rel.Tag)
	if err := c.Apply(rel, exe); err != nil {
		fatal(err)
	}
	fmt.Printf("updated mir → %s\n", rel.Tag)
}
```
Also extend the `--version` usage string at line 50 to mention `self-update`.

- [ ] **Step 3: Add the same `self-update` subcommand to `mir-agent`**

In `go/cmd/mir-agent/main.go`, mirror Step 2 but with `selfupdate.New(repoSlug, "mir-agent")`, printing `mir-agent`, and a `case "self-update": cmdSelfUpdate(os.Args[2:])` in its dispatch. Update its usage string too.

- [ ] **Step 4: Fire the startup notice from `mir` interactive commands**

In `go/cmd/mir/main.go`, inside `cmdList` (a cheap, interactive command), after loading succeeds, add a non-blocking notice:
```go
	selfupdate.New(repoSlug, "mir").MaybeNotify(os.Stderr, updateCachePath(*dir), version.Version, 24*time.Hour)
```
(`cmdList` already has `*dir`; `time` is already imported.)

- [ ] **Step 5: Fire the startup notice from `mir-agent up`**

In `go/cmd/mir-agent/main.go`, inside `cmdUp`, right after the `mir-agent up:` print line (~line 155):
```go
	selfupdate.New(repoSlug, "mir-agent").MaybeNotify(os.Stderr, updateCachePath(*dir), version.Version, 24*time.Hour)
```

- [ ] **Step 6: Build and smoke-test**

Run:
```bash
cd go && go build ./... && go vet ./cmd/...
```
Expected: clean build, no vet errors.
Run: `cd go && go run ./cmd/mir self-update` (with no network mock this hits real GitHub; before any release exists it prints a "github releases: 404" error — that is expected and acceptable until the first tag).

- [ ] **Step 7: Commit**

```bash
git add go/cmd/mir/main.go go/cmd/mir-agent/main.go
git commit -m "feat(cli): self-update subcommand + startup update notice"
```

---

## Task 8: Opt-in auto-update for `mir-agent` (Layer 3) with idle gating

**Files:**
- Modify: `go/internal/agent/runtime.go`
- Create: `go/internal/agent/runtime_active_test.go`
- Modify: `go/cmd/mir-agent/main.go`

- [ ] **Step 1: Write the failing test for the active-session counter**

`go/internal/agent/runtime_active_test.go`:
```go
package agent

import "testing"

func TestActiveSessionsCounter(t *testing.T) {
	rt := &Runtime{}
	if rt.ActiveSessions() != 0 {
		t.Fatalf("fresh runtime active=%d", rt.ActiveSessions())
	}
	rt.sessionStarted()
	rt.sessionStarted()
	if rt.ActiveSessions() != 2 {
		t.Fatalf("after 2 starts active=%d", rt.ActiveSessions())
	}
	rt.sessionEnded()
	if rt.ActiveSessions() != 1 {
		t.Fatalf("after 1 end active=%d", rt.ActiveSessions())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/ -run TestActiveSessionsCounter -v`
Expected: FAIL — `sessionStarted` / `ActiveSessions` undefined.

- [ ] **Step 3: Add the counter to the Runtime**

In `go/internal/agent/runtime.go`, add `"sync/atomic"` to imports, an `active int64` field to the `Runtime` struct, and these methods:
```go
func (rt *Runtime) sessionStarted() { atomic.AddInt64(&rt.active, 1) }
func (rt *Runtime) sessionEnded()   { atomic.AddInt64(&rt.active, -1) }

// ActiveSessions reports the number of in-flight owner sessions. Auto-update
// uses this to defer a binary swap until the agent is idle.
func (rt *Runtime) ActiveSessions() int { return int(atomic.LoadInt64(&rt.active)) }
```
Then bracket the per-offer handling in `handleOffer` (the function that serves a connected owner) with `rt.sessionStarted()` / `defer rt.sessionEnded()` at its top so the counter tracks live sessions.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/ -run TestActiveSessionsCounter -v`
Expected: PASS.

- [ ] **Step 5: Add the `--auto-update` flag + background loop to `mir-agent up`**

In `go/cmd/mir-agent/main.go` `cmdUp`, register the flag:
```go
	autoUpdate := fs.Bool("auto-update", os.Getenv("MIR_AUTO_UPDATE") == "1", "opt-in: automatically self-update when idle")
```
After `rt := agent.NewRuntime(...)` and before `rt.Up(ctx)`, start the loop when enabled:
```go
	if *autoUpdate {
		go autoUpdateLoop(ctx, rt, *dir)
	}
```
Add the loop function (uses the `selfupdate`/`version` imports already added in Task 7):
```go
// autoUpdateLoop checks for a newer release every 12h and applies it only when
// no owner session is active, then re-execs into the new binary. Opt-in.
func autoUpdateLoop(ctx context.Context, rt *agent.Runtime, dir string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	c := selfupdate.New(repoSlug, "mir-agent")
	check := func() {
		if rt.ActiveSessions() > 0 {
			return // defer until idle
		}
		rel, err := c.Latest()
		if err != nil || !selfupdate.IsNewer(version.Version, rel.Tag) {
			return
		}
		if err := c.Apply(rel, exe); err != nil {
			fmt.Fprintf(os.Stderr, "mir-agent: auto-update failed: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "mir-agent: updated → %s, restarting\n", rel.Tag)
		_ = selfupdate.ReExec(exe, os.Args, os.Environ())
	}
	check() // once at startup (after serving begins; gated on idle)
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
```
(`context`, `fmt`, `os`, `path/filepath`, `time` are already imported in this file.)

- [ ] **Step 6: Build + full test suite**

Run:
```bash
cd go && go build ./... && go test ./...
```
Expected: clean build; all packages PASS (including the crypto/noise interop vectors — unaffected by this change).

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/runtime.go go/internal/agent/runtime_active_test.go go/cmd/mir-agent/main.go
git commit -m "feat(agent): opt-in --auto-update with idle gating + re-exec"
```

---

## Task 9: Docs — install & update instructions

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add an Install section to `README.md`**

Add (adapt to the README's existing tone/structure):
```markdown
## Install

    # client (mir)
    curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh

    # agent on a machine you want to reach
    curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh -s -- --agent

Pin a version with `MIR_VERSION=v0.1.0`, change the target dir with
`INSTALL_DIR=/usr/local/bin`.

## Updating

`mir` and `mir-agent` check for a newer release at most once a day and print a
one-line notice. Update on your command:

    mir self-update
    mir-agent self-update

Disable the check with `MIR_NO_UPDATE_CHECK=1`. For unattended machines, opt in
to automatic updates (applied only when no session is active):

    mir-agent up --auto-update      # or MIR_AUTO_UPDATE=1
```

- [ ] **Step 2: Verify the Makefile still builds (sanity)**

Run: `make build`
Expected: `bin/mir`, `bin/mir-agent`, `bin/mir-signal` rebuilt with no errors.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: install + self-update instructions"
```

---

## Done criteria

- `cd go && go test ./...` is green (version, selfupdate, agent counter, plus the untouched crypto vectors).
- `sh test/install_test.sh` prints `OK install_test`.
- `goreleaser check` validates and `goreleaser release --snapshot --clean --skip=publish` produces 12 archives + `checksums.txt`.
- `mir --version` / `mir-agent --version` print a stamped version.
- `mir self-update` / `mir-agent self-update` exist and report "already up to date" against the latest release.
- The first real release is cut by pushing a tag (`git tag v0.1.0 && git push origin v0.1.0`) — left to the user, not done during implementation.

## Known limitation (carried from the spec)

Pull-based update needs the binary to reach `github.com` /
`objects.githubusercontent.com`. Agents on networks that only allow the relay
fall back to manual updates; push-via-relay is a future, separately-specced
follow-up.

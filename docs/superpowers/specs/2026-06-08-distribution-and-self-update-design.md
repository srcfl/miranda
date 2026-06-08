# Miranda — Distribution & Self-Update (Design)

**Date:** 2026-06-08
**Status:** Approved design, pending implementation plan
**Scope:** Friendly install + self-update for `mir` and `mir-agent`

## One-line

A public, install-friendly distribution story for Miranda: prebuilt binaries on
GitHub Releases, a `curl | sh` installer, and a pull-based self-update that tells
you when a new version exists and lets you apply it — manually by default, or
automatically as a per-machine opt-in.

## Goal

Anyone can install `mir` (client) or `mir-agent` (machine agent) with a single
`curl` one-liner, and keep it current without manually chasing GitHub Releases.
Updates are **pull-based** (the binary checks GitHub itself), **verified** by
SHA256 checksum over HTTPS before any swap, and **never applied without consent**
unless auto-update is explicitly enabled.

## Non-goals (YAGNI)

- `mir-signal` (the relay) is **out of scope** for `curl|sh` + self-update. It is
  operated by Sourceful as a service and updated through the normal deploy path
  (systemd / container image). It is still *built and published* in the same
  release so the server side can pull a pinned binary, but it has no installer and
  no self-update subcommand.
- **Push-based updates** (relay tells agents to update). Not in v1. The relay never
  participates in the update flow — see "Known limitation".
- **Cosign / signature verification.** v1 uses checksum-over-HTTPS. The verification
  step is designed so cosign can be layered in later without rewriting self-update.
- **Homebrew tap.** Possible later as a convenience for Mac users; not required.
- **Auto-update on by default.** Default is notify-only.

## Decisions (locked)

| Decision | Value |
|---|---|
| Scope of friendly install + self-update | `mir` + `mir-agent` |
| Update model | Pull — the binary checks GitHub Releases itself |
| Verification | SHA256 checksum fetched over HTTPS |
| Default update behavior | Notify only ("update available, run …") |
| Auto-update | Opt-in, per machine, off by default |

## Architecture

Three pieces, each independently understandable:

1. **Release pipeline** — GoReleaser + GitHub Actions. A pushed semver tag
   produces a GitHub Release with cross-platform archives and a `checksums.txt`.
2. **Installer** — a single `install.sh` in the repo root, fetched via `curl` and
   piped to `sh`. Detects OS/arch, downloads the right asset, verifies, installs.
3. **Self-update** — a shared `internal/selfupdate` package consumed by both
   `mir` and `mir-agent`, plus a shared `internal/version` for the embedded
   version string.

Self-update talks **directly to GitHub** (`api.github.com`,
`objects.githubusercontent.com`), never through the relay. The "relay sees no
plaintext" invariant is therefore untouched by this feature.

## 1. Release pipeline

- **`.goreleaser.yaml`** builds all three binaries (`mir`, `mir-agent`,
  `mir-signal`) for the matrix `darwin/linux × arm64/amd64`.
- Archives named predictably, e.g. `mir_<version>_<os>_<arch>.tar.gz`, plus a
  single `checksums.txt` (SHA256) covering every archive. The installer and
  self-update both rely on this naming being stable.
- **`internal/version`** holds `var Version = "dev"` (plus `Commit`, `Date`),
  overridden at build time via ldflags. GoReleaser sets these automatically.
  Today `mir --version` only prints usage — each cmd must wire `--version` to
  print the embedded version and exit.
- **GitHub Actions workflow** (`.github/workflows/release.yml`) triggers on tags
  matching `v*`, runs GoReleaser, and publishes the Release. CI uses Go 1.26.x to
  match the module.
- Versioning is **semver tags** (`v0.1.0`). Pushing a tag is the entire release
  ritual.

## 2. Installer (`install.sh`)

Invoked as:

```sh
curl -fsSL https://raw.githubusercontent.com/srcfl/miranda/main/install.sh | sh
```

Behavior:

- Detects OS (`darwin`/`linux`) and arch (`arm64`/`amd64`); aborts with a clear
  message on anything unsupported.
- Resolves the version: latest release via the GitHub API by default, or a pinned
  `MIR_VERSION=v0.1.0` env override.
- Downloads the matching archive **and** `checksums.txt` over HTTPS, verifies the
  SHA256, then extracts.
- **Default installs `mir`** (the laptop/client case). Flags:
  - `--agent` → install `mir-agent` instead
  - `--all` → install both
  - passed through the pipe as `sh -s -- --agent`
- Installs to `~/.local/bin` by default; override with `INSTALL_DIR=/usr/local/bin`.
- After install: if the target dir is not on `PATH`, warn and print the exact
  `export PATH=...` line to add.
- Idempotent: re-running upgrades in place.

## 3. Self-update

A shared `internal/selfupdate` package used by both binaries. Three layers, from
passive to active:

### Layer 1 — Update notice (on by default, disablable)

- On normal runs, both binaries perform a **cheap, throttled** check against the
  GitHub Releases API: result cached on disk (e.g. under the config dir), checked
  at most once per 24h, run in the background so it never delays a command.
- If a newer version exists, one line is written to **stderr**:
  ```
  Update available: v0.1.0 → v0.2.0   run: mir self-update
  ```
- Never writes to stdout; never breaks scripted/JSON output.
- Disable entirely with `--no-update-check` or `MIR_NO_UPDATE_CHECK=1` (for
  air-gapped or privacy-sensitive setups).

### Layer 2 — Manual update (always available)

- `mir self-update` / `mir-agent self-update`.
- Queries the GitHub Releases API for the latest tag, compares semver against the
  embedded `internal/version`.
- If newer: downloads the matching asset + `checksums.txt` over HTTPS, verifies
  SHA256, writes a temp file alongside the running binary, atomically `rename`s it
  over the current binary, sets the exec bit.
- Prints `already up to date` or `updated v0.1.0 → v0.2.0`.

### Layer 3 — Auto-update (opt-in, off by default)

- Enabled per machine: `mir-agent up --auto-update` or `MIR_AUTO_UPDATE=1`
  (same mechanism available to `mir` for those who want it).
- When enabled — and only then — the binary applies updates automatically:
  - `mir-agent`: checks at startup (applies **before** it begins serving) and
    periodically (default every 12h) while running.
  - Periodic application happens **only when no client session is active**; if a
    session is in progress, the update is deferred until idle.
  - After swapping its binary, the agent re-executes via `syscall.Exec` so the
    PID — and any systemd/supervisor wrapping it — survives in place.
- With auto-update off (the default), Layers 1 and 2 are all that happen: notify,
  then update on the user's command.

## Verification

- Single mechanism in v1: fetch `checksums.txt` from the release over HTTPS,
  compute SHA256 of the downloaded archive/binary, compare. Mismatch → abort, do
  not swap.
- The verification step is isolated behind a small interface so a cosign/provenance
  check can be added later without touching the download/swap logic.

## Atomic swap & restart

- Write the new binary to a temp path in the **same directory** as the target (so
  `rename` is atomic on the same filesystem), `chmod +x`, then `os.Rename` over the
  running binary. On Unix, replacing the file of a running process is safe.
- `mir-agent` under a supervisor: after a successful swap, `syscall.Exec` replaces
  the current process image with the new binary, preserving PID and FDs.
- `mir` (interactive client): manual `self-update` simply exits after swapping; the
  user re-runs.

## Error handling

- Any network failure during a check → silent for Layer 1 (no notice, no error
  noise), explicit error for Layer 2 (`mir self-update` reports "could not reach
  GitHub").
- Checksum mismatch or partial download → abort before swap; the running binary is
  never touched until the new one is fully verified on disk.
- Unsupported OS/arch in the installer → clear, early failure.
- GitHub API rate limits (60/h unauthenticated per IP): the 24h cache for Layer 1
  and the 12h cadence for Layer 3 keep usage far under the limit.

## Known limitation — network reachability

Pull-based update requires the binary to reach `github.com` /
`objects.githubusercontent.com`. On hardened networks that only permit traffic to
the relay, auto-update cannot work; those machines fall back to manual updates (or
a future push-via-relay design). This is an accepted v1 trade-off, recorded here so
it is not a surprise.

## Testing

- `internal/version`: ldflags override is reflected by `--version`.
- `internal/selfupdate`: version comparison (semver), asset-name resolution per
  os/arch, checksum verification (good + tampered fixtures), atomic-swap against a
  temp binary. GitHub API responses stubbed via an injected HTTP client / fake
  server — no live network in tests.
- Throttle/cache logic: a newer version notifies; within the cache window it does
  not re-hit the API.
- `install.sh`: a lightweight shell test (e.g. against a fake release dir or a
  recorded fixture) covering os/arch detection and the checksum-mismatch abort
  path.
- Auto-update gating: periodic apply is skipped while a session is active.

## Out-of-scope follow-ups (each its own spec later)

- Cosign keyless signing + provenance verification.
- Push-via-relay updates for relay-only-reachable agents.
- Homebrew tap and/or `go install` documentation.
- `mir-signal` deploy automation (systemd/container) — separate ops concern.

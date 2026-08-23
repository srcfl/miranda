# Releasing and independently verifying Miranda

Miranda's release boundary is deliberately fail-closed: CI tests both language
implementations, builds deterministic binaries, signs the checksum manifest with
GitHub OIDC/cosign, and only then publishes through GoReleaser.

## Maintainer release gate

1. Start from a clean commit on the intended release branch.
2. Run:

   ```bash
   cd go && go test ./... && go test -race -count=1 ./...
   cd ../web && npm ci && npm test
   cd .. && ./scripts/verify-reproducible.sh
   goreleaser check
   goreleaser release --snapshot --clean --skip=sign
   ```

3. Review `SECURITY.md`, migration notes, and the diff since the previous tag.
4. Tag `vX.Y.Z`. The release workflow repeats the Go/web/reproducibility gates,
   builds macOS/Linux amd64/arm64 archives, writes `checksums.txt`, and keyless-signs
   that manifest. Do not upload replacement assets by hand.

## Independent binary reproduction

Use the exact Go toolchain from `go/go.mod` (currently Go 1.26.6), a clean clone,
and the tagged commit:

```bash
git clone https://github.com/srcfl/miranda
cd miranda
git checkout vX.Y.Z
git status --porcelain   # must print nothing
./scripts/verify-reproducible.sh
```

The script builds `mir`, `mir-agent`, and `mir-signal` twice with separate Go build
caches, `CGO_ENABLED=0`, `-trimpath`, no embedded VCS/build ID, and the commit's
timestamp. It fails unless both copies are byte-identical and prints SHA-256 for
the current OS/architecture.

To compare with a release, first verify `checksums.txt` using the identity pinned
in `install.sh`, verify the archive digest, extract the matching binary, and compare
its SHA-256 with the local result. A mismatch is a release blocker.

## What this proves

Reproduction detects hidden or nondeterministic changes between tagged source and
binary output on the same toolchain/platform. Cosign ties the published manifest
to this repository's tag workflow. Neither mechanism proves protocol correctness,
endpoint safety, or absence of vulnerabilities; those require review and audit.

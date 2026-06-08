# Miranda — agent notes

Browser → blind relay → agent terminal, passkey-authenticated, E2E encrypted.
(Project: **Miranda**. Binaries: `mir` client, `mir-agent`, `mir-signal` relay.
Go module path stays `github.com/srcful/terminal-relay/go` — internal, not the
brand. See `docs/naming.md`.)

## Invariants (do not break)
- The relay never sees plaintext. Only `owner_id`, `machine_id`, metadata.
- Go and JS crypto MUST stay byte-identical — `testdata/` vectors are the gate.
  After changing any handshake/derivation code, run `cd go && go test ./...` and
  `cd web && npm test`; if vectors legitimately change, regenerate with
  `UPDATE_VECTORS=1 go test ./internal/noise/ -run TestInteropVectorsStable`.
- Owner identity derives from the passkey `prf` output; never store the owner
  private key — re-derive it per device.

## Layout
- `go/` — `mir-agent`, `mir-signal`, the `mir` client, shared `internal/` packages.
- `web/` — browser client (vanilla JS + xterm.js).
- `docs/superpowers/{specs,plans}/` — design + plans.

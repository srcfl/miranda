# terminal-relay

A terminal you reach from any browser by authenticating with a passkey — like
SSH, without thinking SSH. End-to-end encrypted (the relay is a blind pipe),
persistent tmux sessions, passkey-derived identity synced across your devices.

See `docs/superpowers/specs/` for the design and `docs/superpowers/plans/` for
the implementation roadmap.

## Crypto core (Plan 1)

- `go/internal/noise` — `Noise_KK_25519_ChaChaPoly_SHA256` over `flynn/noise`.
- `web/src/noise` — the same handshake, from-spec on `@noble`.
- `go/internal/identity` + `web/src/identity` — `prf` → X25519 owner key.
- `testdata/` — deterministic vectors certifying Go↔JS interop.

Run the tests:

    cd go && go test ./...
    cd web && npm install && npm test

## Signaling server (Plan 2)

`go/cmd/tr-signal` — brokers the WebRTC handshake (SDP offer/answer) between a
browser and an agent matched by `{owner_id, machine_id}`. It carries **no terminal
data**: terminal bytes flow peer-to-peer over a WebRTC DataChannel (strict P2P,
STUN-only, no TURN), with the Plan-1 Noise channel running inside. Once the
DataChannel is up, the server is out of the loop.

    cd go && go run ./cmd/tr-signal --addr :8443

Endpoints: `/agent/signal`, `/attach` (both WSS), `/healthz`.

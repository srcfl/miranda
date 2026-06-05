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

## Agent + local dev (Plan 3)

`go/cmd/tr-agent` — the machine side. It registers on `tr-signal`, accepts an
attach, opens a P2P WebRTC DataChannel, runs the Noise `KK` responder, and bridges
it to a real PTY. Production launches `tmux new -A -s main` (persistence; install
with `brew install tmux`); `--shell sh` runs a plain shell.

Local loop:

    make build
    ./bin/tr-signal --addr :8443 &
    ./bin/tr-agent enroll --signal http://localhost:8443
    ./bin/tr-agent pair-dev --owner-pub <owner-hex>   # dev pre-pin (QR pairing is Plan 4)
    ./bin/tr-agent up --shell sh

The full path — browser-stand-in → `tr-signal` → real shell over P2P — is proven
hermetically by `go test ./internal/agent/ -run TestEndToEnd`.

Note: interactive QR/token pairing is built in Plan 4 alongside the browser.

## CLI client `trm` (Plan 4)

`go/cmd/trm` — `trm attach <machine>` opens a P2P terminal to one of your machines
from your own terminal. (Named `trm` — "terminal relay multiplexer" — to avoid the
collision with the POSIX `tr` utility.) Identity is a local owner key
(`~/.terminal-relay/owner.json`); machines are pinned by host key in `machines.json`.

Install the CLIs onto your PATH with `make install` (→ `~/.local/bin`), or run them
from `./bin/`.

Local loop (all on one Mac):

    make build
    ./bin/tr-signal --addr :8443 &
    # machine side:
    ./bin/tr-agent enroll --signal http://localhost:8443      # prints machine_id + host_pub
    ./bin/trm keygen                                          # prints owner pub
    ./bin/tr-agent pair-dev --owner-pub <owner-pub>           # machine trusts you
    ./bin/tr-agent up --shell sh &                            # (or tmux for persistence)
    # client side:
    ./bin/trm add-machine --name box --id <machine_id> --host-pub <host_pub> --signal http://localhost:8443
    ./bin/trm attach box                                     # real shell over P2P

The full client path is proven hermetically by
`go test ./internal/client/ -run TestEndToEnd`.

### Pairing (Plan 5)

One tap instead of copying keys by hand:

    # on the machine:
    tr-agent pair --signal http://localhost:8443      # prints a code + QR, then waits
    # on the client:
    trm pair <code>                                   # done — machine added

Under the hood a one-time token is the PSK of an NNpsk0 handshake brokered (blind)
through `tr-signal` by `roomID = H(token)`; the two sides exchange and pin their
static keys. The signaling server never sees the token or any key.

### Multi-machine (Plan 4b)

`trm attach <m1> <m2> ...` attaches several machines at once and multiplexes them
onto your terminal — one in focus, the rest live in the background. Switch with the
prefix key **Ctrl-O** (layout-independent; change it with `--prefix`, e.g.
`--prefix ctrl-a`) then:

- `1`–`9` — focus that machine
- `n` — next machine
- `q` — quit (detach all)
- `Ctrl-O` again — send a literal Ctrl-O to the focused machine

Each machine keeps its own tmux (windows/panes + persistence); this layer only
chooses which machine is in focus. Switching clears the screen and nudges the
focused machine's tmux to redraw.

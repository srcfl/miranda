# netsim — the NAT matrix, on your laptop

One command puts a real `mir` agent and a real client behind separate simulated
NATs, drives the production attach path between them, yanks the client's uplink
mid-session, and writes down what happened.

```bash
make netsim                 # or: cd netsim && ./run.sh
./run.sh prc-prc flip-prc   # just those scenarios
./run.sh --list             # what the scenarios are
```

Results land in `netsim/results/results.md` (committed) and
`netsim/results/raw/*.json` (per-run scratch). A full matrix takes about seven
minutes; the image build is cached, so a re-run is faster.

It measures the three numbers the v0.8 beta gates ask for:

| Number | What it is |
|---|---|
| **attach** | `client.Attach` start until the agent's shell echoes our probe — a full round trip through the PTY, not just a socket. |
| **resume** | The moment the client's uplink is swapped until the resumed session carries a byte again. |
| **continuation** | Whether the job that was running before the flip was still running after it. |

## Topology

```
   lan_agent 10.88.10.0/24          pub 10.88.0.0/24          lan_wifi 10.88.20.0/24
  ┌────────────────┐          ┌──────────────────────┐        ┌────────────────┐
  │ agent .10.5    ├──nat─────┤ relay  .0.10         ├───nat──┤ client  .20.5  │
  │ mir up (tmux)  │   .0.2   │ coturn .0.20         │  .0.3  │ driver  .30.5  │
  └────────────────┘          └──────────────────────┘        └───────┬────────┘
                                                                 nat .0.4
                                                          lan_cell 10.88.30.0/24
```

Agent and client share no network. The LAN segments are `internal`, and each NAT
router drops forwarding to the other LANs — the Docker host has a route to every
bridge it created, so without that drop a peer could reach the other's *private*
address straight through the host and never touch a NAT. With it, every path the
two peers can find goes out through a NAT, which is the whole point.

The client has two uplinks behind two different NATs. The second one is held
administratively down, so it gathers no ICE candidate until the flip.

## What each NAT approximates

`netsim/scripts/netsim-router.sh` builds each NAT from two independent choices —
how the mapping is allocated, and what is allowed back in — because that pair is
what actually decides whether two peers can hole-punch.

| Mode | Rules | Approximates |
|---|---|---|
| `none` | routed, no translation | a host with a routable address (`open-agent` puts the agent on `pub`) |
| `prc` | `SNAT --to-source <router public ip>` for UDP plus a matching `DNAT` back in, and `FORWARD` limited to established conntrack flows | a **port-restricted cone NAT** — the ordinary home router. One internal port keeps one external port whatever it talks to, but only the exact peer address *and* port it already spoke to may answer. |
| `sym` | `MASQUERADE --random-fully`, same inbound restriction | a **symmetric NAT** — much carrier-grade NAT and plenty of corporate gear. The external port is drawn afresh per destination, so the mapping a peer learns from STUN is never the mapping it will be contacted on. |

`prc` uses an explicit `SNAT` rather than `MASQUERADE` on purpose: port
preservation, and therefore endpoint-independent mapping, is then guaranteed by
construction instead of left to the kernel's port-allocation heuristics, which do
not preserve ports on every host we run this on. Getting this wrong is easy and
quiet — the first version of this harness "passed" `prc-prc` while actually
routing around both NATs.

**There is no full-cone mode.** Expressing one means letting the `DNAT` accept
NEW inbound UDP, and those conntrack entries then collide with the SNAT mapping
the node is about to create for the same peer: the mapping moves, ICE's checks
stop being symmetric, and every candidate pair fails. Measured, not assumed — the
mode was built, it failed 3/3, and it was removed. `open-agent` covers the
always-reachable case instead.

`BLOCK_PEER_UDP=1` additionally drops every forwarded UDP flow except the ones to
and from coturn, so no direct path can exist at all and ICE has to relay. That is
how `turn-only` isolates the TURN fallback.

## Scenarios

| Scenario | Agent | Client | ICE | Flip |
|---|---|---|---|---|
| `open-agent` | routable | port-restricted | STUN | — |
| `lan-direct` | client's own subnet | agent's own subnet | STUN (host pair) | — |
| `prc-prc` | port-restricted | port-restricted | STUN | — |
| `sym-sym-stun` | symmetric | symmetric | STUN | — (expected to fail) |
| `sym-sym-turn` | symmetric | symmetric | TURN | — |
| `turn-only` | port-restricted, peer UDP blocked | same | TURN | — |
| `flip-prc` | port-restricted | port-restricted | STUN | yes |
| `flip-turn` | symmetric | symmetric | TURN | yes |

`sym-sym-stun` is expected to fail, and the harness says so rather than hiding
it. It is the case STUN cannot solve, and it is why the relay hands out TURN
credentials at all.

TURN is offered only in the scenarios that name it: `netsim-relay.sh` starts
`mir-signal` with `--turn-url` only when `NETSIM_TURN=1`, so a "strict P2P"
scenario really is strict.

## How the driver works

The driver is a Go **test binary** built from `go/internal/netsim`
(`go test -c`). That is deliberate. `internal/client` keeps the owner root in the
OS keychain and accepts the `MIR_TEST_KEYCHAIN_DIR` override only when `argv[0]`
ends in `.test`, so building it this way lets a headless Linux container hold an
owner identity without weakening the production storage rule. Nothing in `mir`,
`mir-agent` or `mir-signal` changed for any of this.

It runs the real thing throughout: `client.Attach`, the real locator race, the
real `client.ReconnectLoopWith` under the production policy, and a real `mir up`
serving a real tmux session on the other side. The only concession is
provisioning — `TestNetsimProvision` writes the pairing outcome (owner pin,
owner-signed registration authorization, sealed registry record, pinned host key)
straight into the shared state volume instead of driving the interactive `mir
pair` handshake.

Continuation is measured, not assumed. After the first attach the driver starts a
counting heartbeat inside the tmux session; after the flip it requires a
heartbeat with a *higher* counter, which can only exist if the job ran right
through the outage.

The flip itself is `netsim-flip.sh`: bring the standby uplink up, move the default
route, take the old link down. New interface, new address, new NAT mapping — a
Wi-Fi-to-cellular flip. It toggles, so consecutive reps flip back and forth.

## Tuning

Environment variables the driver reads (all optional):

| Variable | Default | Meaning |
|---|---|---|
| `NETSIM_REPS` | 3 | measurements per scenario; exporting it overrides every scenario's default |
| `NETSIM_REP_BUDGET` | 90s | ceiling for one measurement |
| `NETSIM_FLIP_AFTER` | 7s | how long to hold the session before flipping (must exceed the policy's 5s `MinHealthy`) |
| `NETSIM_MAX_FAILURES` | production default (7) | reconnect failure budget |
| `NETSIM_ICE_DEBUG` | unset | set to `1` to print gathered ICE candidates and state changes |

## What the runs said

Full table in [`results/results.md`](results/results.md).

**The first run found the gate open, and where.** Resume was ~4.1 s, and
detection was nearly all of it: ~3.23 s to notice the link was dead, then ~0.83 s
(direct) or ~1.23 s (TURN) to carry bytes again. The 3.23 s was by design —
`peer.iceDisconnectedTimeout` (2 s) plus `peer.LinkGrace` (1 s) — and that
constant's own comment had predicted "~3 s". Since the redial was already
sub-second, the R1 gate of under 3 s could only come out of detection.

**Retuning detection closed it (#83).** With `iceDisconnectedTimeout` at 1 s and
`LinkGrace` at 500 ms, measured detection is ~1.75 s and resume is 2.56 s direct
/ 2.99 s relayed. Detection reads ~250 ms above the 1.5 s the arithmetic implies
because pion checks liveness on a keepalive-driven ticker, so the `disconnected`
transition lands up to one 500 ms interval late.

**The relayed path meets the gate without margin.** `flip-turn` resumes cluster
between 2.96 s and 3.02 s, so roughly half of them land just over 3 s. Its extra
~400 ms over the direct path is the TURN allocation in the redial. Holding the
gate on relayed sessions with room to spare means making that redial faster,
which detection tuning cannot do.

**Watch for a ~1 s TURN retransmit.** Occasionally a dial or redial on a TURN
path takes ~1 s longer than its neighbours. It predates the detection retune (it
shows up in the original `turn-only` row) and looks like an ICE/TURN
retransmission timer. When it lands on a resume, that rep reads ~4 s.

**Continuation held every time**, in 18/18 flips, on the direct path and the
relayed one.

## Caveats

Read the numbers as *relative*, not absolute. Every link here is a Docker bridge
with a round-trip time near 0.1 ms, so the transport is free and what remains is
protocol work: handshakes, gathering, timers. Real networks add their own
latency on top. What the harness is good at is comparing paths — cone versus
symmetric, direct versus relayed — and catching the day one of them stops
working.

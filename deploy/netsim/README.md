# NAT-sim harness

Local Docker/OrbStack harness that puts a `mir-agent` and a `mir` client behind
**two separate NAT routers** (no shared network) with `mir-signal` + a STUN server
on a public segment — to test real WebRTC NAT traversal locally, before deploy.

```
   lan_a (internal)            pub                    lan_b (internal)
  ┌──────────────┐      ┌──────────────────┐        ┌──────────────┐
  │ agent        │─nat_a│ signal  coturn   │nat_b───│ client       │
  │ 10.88.10.5   │      │ 10.88.0.10  .0.20│        │ 10.88.20.5   │
  └──────────────┘      └──────────────────┘        └──────────────┘
   default route via nat_a    (STUN)        default route via nat_b
```

agent (lan_a) and client (lan_b) share no network; each NAT drops forwarding to
the other's private LAN, so **host candidates are unroutable** and the only
possible path is the STUN-discovered **srflx** (public NAT mapping). No TURN.

## Run

```bash
cd deploy/netsim
./run.sh           # builds the image, brings up the topology, attaches across the NATs
docker compose down -v   # tear down
```

`MIR_ICE_DEBUG=1` is set on the agent and client, so the gathered ICE candidates
and connection-state changes are printed (run.sh surfaces them).

## Finding (2026-06-04): strict P2P fails between two symmetric NATs

The harness works and isolates correctly: STUN succeeds, both peers gather their
`srflx` candidates (the NAT routers' public mappings), and host candidates are
confirmed unroutable. **But the hole-punch does not establish**, and `mir attach`
fails with the clean "no direct P2P path (strict P2P, no relay fallback)" error.

Root cause (confirmed by tcpdump on the NAT routers): Linux `iptables` MASQUERADE,
as configured here, behaves as a **symmetric NAT** — it maps the same internal
socket to a *different* external port per destination. So the `srflx` port a peer
discovers via STUN (talking to coturn) does **not** match the source port its
connectivity checks actually use (talking to the other peer). The peers send to
the wrong ports; the NATs' address-and-port-dependent filtering drops the checks.

**Symmetric-NAT ↔ symmetric-NAT cannot be traversed by STUN hole-punching — it
requires a TURN relay.** This is the exact limitation of the "strict P2P, no TURN"
choice, now reproduced locally. Real-world NATs are often friendlier (full/
restricted-cone, which *do* hole-punch), but symmetric NATs are common (much
carrier-grade NAT, some corporate networks), so a meaningful fraction of
real connections would fail without TURN.

Stock Linux/Docker on a single host cannot easily emulate a cone NAT (no
full-cone target in mainline netfilter), so this harness reliably exercises the
*symmetric* case. The cooperative-NAT success path is covered by the loopback
host-candidate E2Es in `go/internal/...` (DataChannel opens directly).

### Implication

Revisit the strict-P2P decision: a TURN fallback (DTLS+Noise keep it blind to
content even when relayed) would make `mir attach` work across symmetric NATs,
at the cost of relaying encrypted bytes for those sessions.

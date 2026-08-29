#!/bin/sh
# A NAT router. It bridges one internal LAN to the shared "pub" segment and
# rewrites addresses on the way out, which is the whole simulation: the node
# behind it has no routable address and no inbound path except one it opened
# itself.
#
# NAT_MODE picks which kind of NAT this is. Each is built from two independent
# choices — how the mapping is allocated, and what is allowed back in — because
# that is what actually decides whether two peers can hole-punch:
#
#   prc   1:1 UDP NAT (SNAT/DNAT on this router's own public address, source port
#         preserved, so one internal port keeps ONE external port whatever it
#         talks to) with inbound UDP restricted to flows already in conntrack —
#         same peer address AND same peer port. A port-restricted cone NAT: the
#         ordinary home router, and the case STUN hole-punching is for.
#
#   sym   MASQUERADE --random-fully: the external port is drawn afresh per
#         destination, so the mapping a peer learns from STUN is not the mapping
#         it will be contacted on. A symmetric NAT — much carrier-grade NAT and
#         plenty of corporate gear. STUN cannot traverse this; TURN can.
#
#   none  no NAT (the node is expected to sit on pub instead).
#
# prc uses an explicit SNAT rather than MASQUERADE so that port preservation —
# and therefore endpoint-independent mapping — is guaranteed by construction
# instead of left to the kernel's port-allocation heuristics, which do not
# preserve ports on every host we run this on.
#
# There is deliberately no full-cone mode. Expressing one means letting the DNAT
# accept NEW inbound UDP, and those conntrack entries then collide with the SNAT
# mapping the node is about to create for the same peer: the mapping moves, ICE's
# checks stop being symmetric, and every pair fails. The `open-agent` scenario
# covers the always-reachable case instead. See netsim/README.md.
#
# BLOCK_NETS lists the other simulated LANs. The Docker host has a route to every
# bridge it created, so without this a node could reach a peer's *private*
# address straight through the host and skip the NAT entirely. Dropping those
# destinations here — the only way out of an `internal` LAN — makes the drawn
# topology the real one.
#
# BLOCK_PEER_UDP=1 additionally drops every forwarded UDP flow except the ones
# to and from the TURN server, so no direct path can exist and ICE has to relay.
set -eu
. /usr/local/bin/netsim-lib.sh

PUB_PREFIX="${PUB_PREFIX:?PUB_PREFIX is required}"
LAN_PREFIX="${LAN_PREFIX:?LAN_PREFIX is required}"
NAT_MODE="${NAT_MODE:-prc}"

PUB_IF="$(iface_for "$PUB_PREFIX")"
LAN_IF="$(iface_for "$LAN_PREFIX")"
PUB_ADDR="$(addr_for "$PUB_PREFIX" | cut -d/ -f1)"
[ -n "$PUB_IF" ] || { echo "netsim-router: no interface in $PUB_PREFIX" >&2; exit 1; }
[ -n "$LAN_IF" ] || { echo "netsim-router: no interface in $LAN_PREFIX" >&2; exit 1; }

IPT="$(pick_iptables)"
"$IPT" -t nat -F POSTROUTING
"$IPT" -t nat -F PREROUTING
"$IPT" -F FORWARD
"$IPT" -P FORWARD ACCEPT

case "$NAT_MODE" in
none)
	log "router: no NAT ($LAN_IF <-> $PUB_IF, routed)"
	;;
sym)
	"$IPT" -t nat -A POSTROUTING -o "$PUB_IF" -j MASQUERADE --random-fully
	log "router: symmetric NAT ($LAN_IF -> $PUB_IF as $PUB_ADDR, per-destination port)"
	;;
prc)
	NODE_IP="${NODE_IP:?NODE_IP is required for a 1:1 NAT mode}"
	"$IPT" -t nat -A POSTROUTING -o "$PUB_IF" -p udp -s "$NODE_IP" -j SNAT --to-source "$PUB_ADDR"
	"$IPT" -t nat -A PREROUTING -i "$PUB_IF" -p udp -d "$PUB_ADDR" -j DNAT --to-destination "$NODE_IP"
	"$IPT" -t nat -A POSTROUTING -o "$PUB_IF" -j MASQUERADE
	log "router: port-restricted cone NAT ($NODE_IP <-> $PUB_ADDR, UDP source port preserved)"
	;;
*)
	echo "netsim-router: unknown NAT_MODE $NAT_MODE" >&2
	exit 1
	;;
esac

for net in ${BLOCK_NETS:-}; do
	"$IPT" -A FORWARD -d "$net" -j DROP
	"$IPT" -A FORWARD -s "$net" -j DROP
done
if [ -n "${BLOCK_NETS:-}" ]; then
	log "router: private LANs unreachable through here: $BLOCK_NETS"
fi

if [ "${BLOCK_PEER_UDP:-0}" = "1" ]; then
	TURN_IP="${TURN_IP:?TURN_IP is required when BLOCK_PEER_UDP=1}"
	"$IPT" -A FORWARD -p udp -d "$TURN_IP" -j ACCEPT
	"$IPT" -A FORWARD -p udp -s "$TURN_IP" -j ACCEPT
	"$IPT" -A FORWARD -p udp -j DROP
	log "router: direct peer UDP blocked; only $TURN_IP may carry UDP"
fi

# Inbound filtering: accept only what matches a flow the node opened itself.
case "$NAT_MODE" in
none) ;;
*)
	"$IPT" -A FORWARD -i "$PUB_IF" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	"$IPT" -A FORWARD -i "$PUB_IF" -j DROP
	log "router: inbound restricted to established flows (same peer address and port)"
	;;
esac

"$IPT" -t nat -S
"$IPT" -S FORWARD

exec tail -f /dev/null

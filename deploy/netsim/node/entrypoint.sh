#!/bin/sh
# Node (agent or client): route everything via the NAT router on its LAN, so the
# only path to the public network (signal + STUN) and to the peer is through NAT.
set -e

if [ -z "$NAT_GW" ]; then
  echo "[node] ERROR: NAT_GW not set" >&2
  exit 1
fi

ip route del default 2>/dev/null || true
ip route add default via "$NAT_GW"
echo "[node] default route via $NAT_GW; addresses:"
ip -o -4 addr show | awk '{print "  " $2 " " $4}'
exec "$@"

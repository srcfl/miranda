#!/bin/sh
# NAT router: forward + masquerade traffic from a private LAN out the public
# interface (the one on 10.88.0.0/24). Endpoint-independent enough for ICE
# UDP hole-punching via the conntrack NAT.
set -e

WAN_IF=$(ip -o -4 addr show | awk '/10\.88\.0\./{print $2; exit}')
if [ -z "$WAN_IF" ]; then
  echo "[nat] ERROR: no interface on the public 10.88.0.0/24 network" >&2
  exit 1
fi

# Block the single-host shortcut: drop forwarding toward the FOREIGN private LANs
# so a host candidate (private IP) is unreachable and ICE must use the srflx
# (public NAT mapping). $BLOCK is a space-separated list of foreign LAN subnets.
for net in $BLOCK; do
  iptables -A FORWARD -d "$net" -j DROP
  echo "[nat] drop forward to foreign LAN $net"
done

iptables -t nat -A POSTROUTING -o "$WAN_IF" -j MASQUERADE
iptables -A FORWARD -j ACCEPT

echo "[nat] forwarding + masquerade out $WAN_IF; conntrack UDP timeouts:"
cat /proc/sys/net/netfilter/nf_conntrack_udp_timeout 2>/dev/null || true
exec sleep infinity

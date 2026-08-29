#!/bin/sh
# Shared helpers for the netsim container scripts. Sourced, never executed.

# iface_for <ipv4 prefix>  ->  the interface holding an address in that prefix.
# Docker hands out eth0/eth1 in an order we do not control, so every script
# identifies its links by subnet instead of by name.
iface_for() {
	ip -o -4 addr show | awk -v p="$1" '$4 ~ "^"p {print $2; exit}'
}

# addr_for <ipv4 prefix>  ->  the CIDR address on that interface (10.88.20.5/24).
addr_for() {
	ip -o -4 addr show | awk -v p="$1" '$4 ~ "^"p {print $4; exit}'
}

# pick_iptables prints the first iptables variant that can actually talk to the
# kernel. Alpine ships the nft-backed binary; some kernels only take the legacy
# one, and a container that silently installed no rules would quietly turn the
# whole scenario into a lie.
pick_iptables() {
	for candidate in iptables iptables-nft iptables-legacy; do
		if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -t nat -L -n >/dev/null 2>&1; then
			echo "$candidate"
			return 0
		fi
	done
	echo "netsim: no working iptables variant in this container" >&2
	return 1
}

log() { echo "netsim[$(hostname)]: $*"; }

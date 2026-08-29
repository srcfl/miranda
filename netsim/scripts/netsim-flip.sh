#!/bin/sh
# Swap the client onto its other uplink: bring the standby link up, move the
# default route to it, and take the old link down. New interface, new address,
# new NAT mapping — the harness's Wi-Fi-to-cellular flip. It toggles, so calling
# it again flips back.
set -eu
. /usr/local/bin/netsim-lib.sh

LINKS_FILE=/run/netsim-links.env
[ -f "$LINKS_FILE" ] || { echo "netsim-flip: no uplinks recorded ($LINKS_FILE)" >&2; exit 1; }
# shellcheck disable=SC1090
. "$LINKS_FILE"
[ -n "${ALT_IF:-}" ] || { echo "netsim-flip: this node has only one uplink" >&2; exit 1; }

if ip link show "$PRI_IF" | grep -q 'state UP'; then
	FROM_IF="$PRI_IF"
	TO_IF="$ALT_IF"; TO_GW="$ALT_GW"; TO_ADDR="$ALT_ADDR"
else
	FROM_IF="$ALT_IF"
	TO_IF="$PRI_IF"; TO_GW="$PRI_GW"; TO_ADDR="$PRI_ADDR"
fi

ip link set "$TO_IF" up
# The address survives a link-down, but re-assert it and wait for the connected
# route so `ip route replace` below cannot lose a race with the kernel.
ip addr replace "$TO_ADDR" dev "$TO_IF" 2>/dev/null || true
i=0
while [ "$i" -lt 50 ]; do
	if ip route show dev "$TO_IF" | grep -q .; then break; fi
	i=$((i + 1))
	sleep 0.02
done

ip route replace default via "$TO_GW" dev "$TO_IF"
ip link set "$FROM_IF" down

echo "flipped $FROM_IF -> $TO_IF, default via $TO_GW"

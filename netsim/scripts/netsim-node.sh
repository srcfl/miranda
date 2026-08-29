#!/bin/sh
# Entrypoint for every container that runs Miranda code. It points the default
# route at this node's NAT router (Docker's own gateway on an `internal` network
# goes nowhere), records the uplinks for netsim-flip.sh, and then execs whatever
# the service was asked to run.
#
# NETSIM_UPLINK_PREFIX / NETSIM_UPLINK_GW  the primary uplink (Wi-Fi).
# NETSIM_ALT_PREFIX / NETSIM_ALT_GW        an optional second uplink (cellular),
#                                          kept administratively down until the
#                                          flip so it gathers no ICE candidate.
#
# Both are optional: a container on the routed pub segment just runs the command.
set -eu
. /usr/local/bin/netsim-lib.sh

LINKS_FILE=/run/netsim-links.env

if [ -n "${NETSIM_UPLINK_PREFIX:-}" ]; then
	PRI_IF="$(iface_for "$NETSIM_UPLINK_PREFIX")"
	PRI_ADDR="$(addr_for "$NETSIM_UPLINK_PREFIX")"
	[ -n "$PRI_IF" ] || { echo "netsim-node: no interface in $NETSIM_UPLINK_PREFIX" >&2; exit 1; }

	while ip route del default 2>/dev/null; do :; done
	ip route replace default via "${NETSIM_UPLINK_GW:?NETSIM_UPLINK_GW is required}" dev "$PRI_IF"
	log "uplink: $PRI_IF ($PRI_ADDR) via $NETSIM_UPLINK_GW"

	{
		echo "PRI_IF=$PRI_IF"
		echo "PRI_GW=$NETSIM_UPLINK_GW"
		echo "PRI_ADDR=$PRI_ADDR"
	} >"$LINKS_FILE"

	if [ -n "${NETSIM_ALT_PREFIX:-}" ]; then
		ALT_IF="$(iface_for "$NETSIM_ALT_PREFIX")"
		ALT_ADDR="$(addr_for "$NETSIM_ALT_PREFIX")"
		[ -n "$ALT_IF" ] || { echo "netsim-node: no interface in $NETSIM_ALT_PREFIX" >&2; exit 1; }
		{
			echo "ALT_IF=$ALT_IF"
			echo "ALT_GW=${NETSIM_ALT_GW:?NETSIM_ALT_GW is required}"
			echo "ALT_ADDR=$ALT_ADDR"
		} >>"$LINKS_FILE"
		# Down, not just unrouted: pion gathers a host candidate from every live
		# interface, and a standby uplink that already had one would make the
		# flip a no-op.
		ip link set "$ALT_IF" down
		log "standby uplink: $ALT_IF ($ALT_ADDR) via $NETSIM_ALT_GW — down until the flip"
	fi
fi

exec "$@"

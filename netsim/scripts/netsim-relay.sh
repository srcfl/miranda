#!/bin/sh
# mir-signal for the harness. TURN is handed out only when the scenario asks for
# it, so a "strict P2P" scenario really is strict: with no TURN credentials on
# the wire, both peers fall back to STUN and must hole-punch or fail.
set -eu

args="--addr :8443"
if [ "${NETSIM_TURN:-0}" = "1" ]; then
	args="$args --turn-url ${NETSIM_TURN_URL:?NETSIM_TURN_URL is required when NETSIM_TURN=1}"
	echo "netsim-relay: issuing ephemeral TURN credentials for $NETSIM_TURN_URL"
else
	unset MIR_TURN_SECRET
	echo "netsim-relay: TURN disabled for this scenario (STUN only)"
fi

# shellcheck disable=SC2086
exec mir-signal $args

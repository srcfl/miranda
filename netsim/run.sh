#!/usr/bin/env bash
# One command for the whole NAT matrix.
#
#   ./run.sh                    every scenario, then the results table
#   ./run.sh prc-prc flip-prc   just those scenarios (raw results are merged)
#   ./run.sh --list             what the scenarios are
#
# Each scenario gets a clean stack: fresh relay, fresh identities, fresh NAT
# rules. That costs a few seconds per scenario and buys results that cannot be
# contaminated by the run before.
set -euo pipefail
cd "$(dirname "$0")"

export COMPOSE_PROFILES=tools,open,lan
RESULTS_DIR="$(pwd)/results"

ALL_SCENARIOS=(open-agent lan-direct prc-prc sym-sym-stun sym-sym-turn turn-only flip-prc flip-turn)

usage() {
	cat <<'EOF'
usage: ./run.sh [--list] [--no-build] [scenario ...]

scenarios
  open-agent     agent on a routable address, client behind a port-restricted NAT (STUN)
  lan-direct     agent on the client's own subnet, no TURN — only a host<->host pair can pass
  prc-prc        both sides behind port-restricted cone NATs (STUN)
  sym-sym-stun   both sides behind symmetric NATs, no TURN — expected to fail
  sym-sym-turn   both sides behind symmetric NATs, relay offers TURN
  turn-only      port-restricted NATs, every direct UDP path blocked — TURN or nothing
  flip-prc       port-restricted NATs; the client's uplink is swapped mid-session (STUN)
  flip-turn      symmetric NATs with TURN; the client's uplink is swapped mid-session
EOF
}

# configure sets everything docker-compose.yml and the driver read for one
# scenario. AGENT_SVC picks which agent container runs.
# A NETSIM_REPS exported by the caller wins over the per-scenario default, so
# `NETSIM_REPS=9 ./run.sh flip-turn` samples a tail properly. Captured once,
# before configure() starts overwriting the variable.
REPS_OVERRIDE="${NETSIM_REPS:-}"

configure() {
	# Defaults; each scenario overrides what it needs.
	AGENT_SVC=agent
	export NETSIM_SCENARIO="$1"
	export NETSIM_AGENT_NAT_MODE=prc NETSIM_CLIENT_NAT_MODE=prc
	export NETSIM_BLOCK_PEER_UDP=0 NETSIM_TURN=0 NETSIM_FLIP=0
	export NETSIM_EXPECT=pass NETSIM_MAX_FAILURES=0 NETSIM_REP_BUDGET=90s
	export NETSIM_REPS="${REPS_OVERRIDE:-3}"
	export NETSIM_NOTE=""

	case "$1" in
	open-agent)
		AGENT_SVC=agent-open
		export NETSIM_ORDER=1 NETSIM_AGENT_NAT="none (routable)" NETSIM_CLIENT_NAT="port-restricted" NETSIM_ICE="STUN"
		export NETSIM_NOTE="One side reachable at a fixed address — the friendliest real case, and the floor for attach latency."
		;;
	lan-direct)
		AGENT_SVC=agent-lan
		export NETSIM_ORDER=2 NETSIM_AGENT_NAT="none (client's subnet)" NETSIM_CLIENT_NAT="none (agent's subnet)" NETSIM_ICE="STUN (host pair)"
		export NETSIM_NOTE="Peers share a subnet and TURN is off, so only an ICE host<->host pair can complete: passing proves direct-on-LAN, with the relay carrying signalling only."
		;;
	prc-prc)
		export NETSIM_ORDER=3 NETSIM_AGENT_NAT="port-restricted" NETSIM_CLIENT_NAT="port-restricted" NETSIM_ICE="STUN"
		export NETSIM_NOTE="Two ordinary home routers. Both sides hole-punch; the relay only carries signalling."
		;;
	sym-sym-stun)
		export NETSIM_AGENT_NAT_MODE=sym NETSIM_CLIENT_NAT_MODE=sym
		export NETSIM_ORDER=4 NETSIM_AGENT_NAT="symmetric" NETSIM_CLIENT_NAT="symmetric" NETSIM_ICE="STUN"
		export NETSIM_EXPECT=fail NETSIM_MAX_FAILURES=1 NETSIM_REP_BUDGET=60s
		export NETSIM_REPS="${REPS_OVERRIDE:-1}"
		export NETSIM_NOTE="The case STUN cannot solve: each mapping is per-destination, so the port a peer learns is never the port it is contacted on. This is why TURN exists."
		;;
	sym-sym-turn)
		export NETSIM_AGENT_NAT_MODE=sym NETSIM_CLIENT_NAT_MODE=sym NETSIM_TURN=1
		export NETSIM_ORDER=5 NETSIM_AGENT_NAT="symmetric" NETSIM_CLIENT_NAT="symmetric" NETSIM_ICE="TURN"
		export NETSIM_NOTE="The same two symmetric NATs, with the relay handing out ephemeral TURN credentials. Noise keeps the relayed bytes opaque."
		;;
	turn-only)
		export NETSIM_BLOCK_PEER_UDP=1 NETSIM_TURN=1
		export NETSIM_ORDER=6 NETSIM_AGENT_NAT="port-restricted, peer UDP blocked" NETSIM_CLIENT_NAT="port-restricted, peer UDP blocked" NETSIM_ICE="TURN"
		export NETSIM_NOTE="Every UDP flow except the one to coturn is dropped, so a direct path cannot exist. Measures the TURN fallback on its own."
		;;
	flip-prc)
		export NETSIM_FLIP=1
		export NETSIM_ORDER=7 NETSIM_AGENT_NAT="port-restricted" NETSIM_CLIENT_NAT="port-restricted (Wi-Fi -> cellular)" NETSIM_ICE="STUN"
		export NETSIM_NOTE="The client's uplink is swapped for a second one behind a different NAT: new interface, new address, new mapping."
		;;
	flip-turn)
		export NETSIM_AGENT_NAT_MODE=sym NETSIM_CLIENT_NAT_MODE=sym NETSIM_TURN=1 NETSIM_FLIP=1
		export NETSIM_ORDER=8 NETSIM_AGENT_NAT="symmetric" NETSIM_CLIENT_NAT="symmetric (Wi-Fi -> cellular)" NETSIM_ICE="TURN"
		export NETSIM_NOTE="The same flip on the path that has to relay: a fresh TURN allocation on the new uplink."
		;;
	*)
		echo "unknown scenario: $1" >&2
		usage >&2
		exit 2
		;;
	esac
}

wait_for() { # wait_for <seconds> <description> <command...>
	local deadline=$((SECONDS + $1)) desc="$2"
	shift 2
	while ((SECONDS < deadline)); do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 0.5
	done
	echo "netsim: timed out waiting for $desc" >&2
	return 1
}

relay_healthy() { docker compose exec -T relay wget -q -O - http://127.0.0.1:8443/healthz; }
agent_registered() { docker compose logs --no-color relay | grep -q 'event=agent_register'; }

run_scenario() {
	local id="$1"
	configure "$id"
	echo
	echo "=== $id  (agent=${NETSIM_AGENT_NAT}, client=${NETSIM_CLIENT_NAT}, ice=${NETSIM_ICE}, flip=${NETSIM_FLIP}) ==="

	docker compose down -v --remove-orphans >/dev/null 2>&1 || true
	docker compose up -d relay coturn nat-agent nat-wifi nat-cell
	wait_for 60 "the relay to answer /healthz" relay_healthy

	docker compose run --rm --no-deps provision
	docker compose up -d "$AGENT_SVC"
	if ! wait_for 90 "the agent to register with the relay" agent_registered; then
		docker compose logs --no-color "$AGENT_SVC" | tail -40 >&2
		return 1
	fi

	local rc=0
	docker compose run --rm --no-deps client || rc=$?
	if ((rc != 0)); then
		echo "netsim: scenario $id reported failures (exit $rc); the raw result records why" >&2
		FAILED+=("$id")
	fi
	# A quick, independent read on whether TURN actually carried this scenario.
	local allocations
	allocations=$(docker compose logs --no-color coturn 2>/dev/null | grep -ci 'allocat' || true)
	echo "    coturn allocation log lines: ${allocations:-0}"
	return 0
}

BUILD=1
TARGETS=()
for arg in "$@"; do
	case "$arg" in
	--list) usage; exit 0 ;;
	--no-build) BUILD=0 ;;
	-h | --help) usage; exit 0 ;;
	-*) echo "unknown flag: $arg" >&2; usage >&2; exit 2 ;;
	*) TARGETS+=("$arg") ;;
	esac
done

FULL_RUN=0
if ((${#TARGETS[@]} == 0)); then
	TARGETS=("${ALL_SCENARIOS[@]}")
	FULL_RUN=1
fi

mkdir -p "$RESULTS_DIR/raw"
if ((FULL_RUN)); then rm -f "$RESULTS_DIR/raw"/*.json; fi

if ((BUILD)); then
	echo "=== building miranda-netsim:dev ==="
	configure "${TARGETS[0]}"
	docker compose build
fi

started=$SECONDS
FAILED=()
for id in "${TARGETS[@]}"; do
	run_scenario "$id"
done

echo
echo "=== results ==="
docker compose run --rm --no-deps report
docker compose down -v --remove-orphans >/dev/null 2>&1 || true
echo
echo "netsim: $((SECONDS - started))s for ${#TARGETS[@]} scenario(s); table in netsim/results/results.md"

# A scenario the driver expects to fail (sym-sym-stun) exits 0, so anything left
# here is a genuine regression and the run says so.
if ((${#FAILED[@]} > 0)); then
	echo "netsim: scenarios that did not connect: ${FAILED[*]}" >&2
	exit 1
fi

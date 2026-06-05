#!/usr/bin/env bash
# NAT-sim smoke: bring up the topology, pair an agent (lan_a) and client (lan_b)
# that share no network, and prove `tr` connects across two NATs (strict P2P, no
# TURN) by running a real command over the hole-punched DataChannel.
set -euo pipefail
cd "$(dirname "$0")"

SIGNAL="http://10.88.0.10:8443"
STUN="stun:10.88.0.20:3478"
# TURN=1 enables the opt-in TURN fallback (expected to PASS even across symmetric NATs).
TURN_ARGS=""
if [ "${TURN:-0}" = "1" ]; then
  TURN_ARGS="--turn turn:10.88.0.20:3478 --turn-user tr --turn-pass trpass"
fi
dc() { docker compose "$@"; }
exec_t() { docker compose exec -T "$@"; }
field() { grep "\"$2\"" | sed 's/.*: "\(.*\)".*/\1/' | head -1; }

echo "== build + up =="
dc up -d --build

echo "== wait for signal =="
for i in $(seq 1 30); do
  if exec_t nat_a wget -qO- "$SIGNAL/healthz" >/dev/null 2>&1; then echo "signal ready"; break; fi
  sleep 1
done

echo "== sanity: agent and client must NOT be directly reachable (isolated NATs) =="
if exec_t client ping -c1 -W1 10.88.10.5 >/dev/null 2>&1; then
  echo "FAIL: client can reach the agent's host IP directly — networks are not isolated, test is invalid" >&2
  exit 1
fi
echo "ok: 10.88.10.5 (agent) is unroutable from the client — only srflx can connect"

echo "== enroll agent =="
exec_t agent tr-agent enroll --name box --signal "$SIGNAL" >/dev/null
CFG=$(exec_t agent cat /root/.terminal-relay/config.json)
MID=$(echo "$CFG" | field x machine_id)
HPUB=$(echo "$CFG" | field x host_pub)
echo "  machine_id=$MID host_pub=${HPUB:0:16}..."

echo "== client keygen =="
exec_t client trm keygen >/dev/null
OPUB=$(exec_t client cat /root/.terminal-relay/owner.json | field x owner_pub)
echo "  owner_pub=${OPUB:0:16}..."

echo "== pair + run agent =="
exec_t agent tr-agent pair-dev --owner-pub "$OPUB" >/dev/null
dc exec -d agent sh -c "tr-agent up --shell sh --signal $SIGNAL --stun $STUN $TURN_ARGS >/tmp/agent.log 2>&1"
sleep 2
[ -n "$TURN_ARGS" ] && echo "== TURN fallback ENABLED =="

echo "== client registers machine =="
exec_t client trm add-machine --name box --id "$MID" --host-pub "$HPUB" --signal "$SIGNAL" >/dev/null

echo "== run a command across the two NATs =="
OUT=$(exec_t client trm run --stun "$STUN" $TURN_ARGS --window 12s box "echo NAT_TRAVERSAL_OK; echo host=\$(hostname)" 2>&1 || true)
echo "---- client output (incl. ICE debug) ----"
echo "$OUT"
echo "---- agent ICE debug ----"
exec_t agent cat /tmp/agent.log 2>/dev/null | grep -i "ice\|machine" | tail -20 || true
echo "----------------"

if echo "$OUT" | grep -q NAT_TRAVERSAL_OK; then
  if [ -n "$TURN_ARGS" ]; then
    echo "PASS: real shell ran across two symmetric NATs via the TURN relay (Noise keeps the relay blind)."
  else
    echo "PASS: real shell ran over a hole-punched direct P2P DataChannel across two NATs (no TURN)."
  fi
  echo "(leave it up to inspect; tear down with: docker compose -f $(pwd)/docker-compose.yml down -v)"
  exit 0
else
  echo "RESULT: no direct path — the strict-P2P hole-punch did NOT establish across these two NATs."
  echo "If the ICE debug above shows srflx candidates were gathered but the state went checking->closed,"
  echo "this is the expected symmetric-NAT limitation: STUN hole-punching cannot traverse symmetric<->"
  echo "symmetric NAT; it needs TURN. See README.md. (This is the harness demonstrating the limitation,"
  echo "not a bug in tr.)"
  exit 2
fi

#!/usr/bin/env bash
# Install + configure coturn (TURN) on the relay host and wire a shared
# static-auth-secret into /etc/mir-signal.env so mir-signal can mint ephemeral
# credentials (coturn "TURN REST API" scheme). Idempotent. The secret is
# generated on the host and NEVER committed.
#
# Run on the relay host as root:
#   sudo bash setup-coturn.sh <public-ip> [realm]
set -euo pipefail
PUBLIC_IP="${1:?usage: setup-coturn.sh <public-ip> [realm]}"
REALM="${2:-relay.sourceful-labs.net}"
PRIVATE_IP="$(hostname -I | awk '{print $1}')"
ENVFILE=/etc/mir-signal.env

# Generate (or reuse) the shared secret. mir-signal reads it from $ENVFILE.
if [ -f "$ENVFILE" ] && grep -q '^MIR_TURN_SECRET=' "$ENVFILE"; then
  SECRET="$(grep '^MIR_TURN_SECRET=' "$ENVFILE" | cut -d= -f2-)"
else
  SECRET="$(openssl rand -hex 32)"
  printf 'MIR_TURN_SECRET=%s\n' "$SECRET" > "$ENVFILE"
  chmod 600 "$ENVFILE"
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y coturn >/dev/null

cat > /etc/turnserver.conf <<EOF
listening-port=3478
fingerprint
use-auth-secret
static-auth-secret=$SECRET
realm=$REALM
external-ip=$PUBLIC_IP/$PRIVATE_IP
min-port=49160
max-port=49200
no-cli
no-tlsv1
no-tlsv1_1
user-quota=12
total-quota=1200
syslog
EOF
chmod 640 /etc/turnserver.conf

echo "TURNSERVER_ENABLED=1" > /etc/default/coturn
systemctl enable coturn >/dev/null 2>&1 || true
systemctl restart coturn
sleep 1
echo "coturn: $(systemctl is-active coturn)  (external-ip $PUBLIC_IP/$PRIVATE_IP, realm $REALM)"
echo "shared secret in $ENVFILE — restart mir-signal with --turn-url turn:$REALM:3478"

# Firewall reminder (Lightsail is managed via the AWS API/console, not ufw):
echo "OPEN PORTS on the Lightsail firewall: 3478 TCP, 3478 UDP, 49160-49200 UDP"

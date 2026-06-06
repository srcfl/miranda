#!/usr/bin/env bash
# Cross-compile tr-signal, upload it to the Lightsail origin, and restart the
# service. Idempotent: also (re)installs the systemd unit.
#
#   HOST=16.171.89.172 KEY=deploy/lightsail/key.pem ./deploy/lightsail/redeploy.sh
set -euo pipefail

HOST="${HOST:?set HOST to the origin IP}"
KEY="${KEY:?set KEY to the SSH private key path}"
USER_="${USER_:-ubuntu}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"

echo "== build tr-signal (linux/amd64, static) =="
( cd "$REPO/go" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/tr-signal-linux ./cmd/tr-signal )

SSHOPTS="-i $KEY -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10"

echo "== upload =="
scp ${=SSHOPTS} /tmp/tr-signal-linux "$USER_@$HOST:/tmp/tr-signal"
scp ${=SSHOPTS} "$REPO/deploy/lightsail/tr-signal.service" "$USER_@$HOST:/tmp/tr-signal.service"

echo "== install + restart =="
ssh ${=SSHOPTS} "$USER_@$HOST" 'sudo bash -s' <<'EOF'
set -e
install -m 0755 /tmp/tr-signal /usr/local/bin/tr-signal
id trsignal >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin trsignal
install -m 0644 /tmp/tr-signal.service /etc/systemd/system/tr-signal.service
systemctl daemon-reload
systemctl enable --now tr-signal
systemctl restart tr-signal
sleep 1
echo "active: $(systemctl is-active tr-signal)"
curl -s -o /dev/null -w "local healthz: %{http_code}\n" http://localhost/healthz
EOF

echo "== public healthz =="
curl -s -m 10 -o /dev/null -w "http://$HOST/healthz -> %{http_code}\n" "http://$HOST/healthz"
echo "done."

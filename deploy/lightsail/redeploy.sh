#!/usr/bin/env bash
# Cross-compile tr-signal and (re)deploy it to the relay host, then restart the
# service. Idempotent: also (re)installs the systemd unit.
#
# Easy mode (uses the `tr-signal` SSH alias in ~/.ssh/config):
#   ./deploy/lightsail/redeploy.sh
#
# Override the target:
#   TARGET=my-alias ./deploy/lightsail/redeploy.sh           # a different SSH alias
#   HOST=1.2.3.4 KEY=~/.ssh/key.pem ./deploy/lightsail/redeploy.sh   # raw host + key
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../.." && pwd)"

if [ -n "${HOST:-}" ]; then
  DEST="${USER_:-ubuntu}@${HOST}"
  SSH=(ssh -i "${KEY:?set KEY when using HOST}" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
  SCP=(scp -i "${KEY}" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
else
  DEST="${TARGET:-tr-signal}" # ~/.ssh/config alias
  SSH=(ssh -o ConnectTimeout=15)
  SCP=(scp -o ConnectTimeout=15)
fi

echo "== build tr-signal (linux/amd64, static) =="
( cd "$REPO/go" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/tr-signal-linux ./cmd/tr-signal )

echo "== package SPA (index.html + src + vendor) =="
tar -C "$REPO/web" -czf /tmp/tr-web.tgz index.html src vendor

echo "== upload to $DEST =="
"${SCP[@]}" /tmp/tr-signal-linux "$DEST:/tmp/tr-signal"
"${SCP[@]}" "$REPO/deploy/lightsail/tr-signal.service" "$DEST:/tmp/tr-signal.service"
"${SCP[@]}" /tmp/tr-web.tgz "$DEST:/tmp/tr-web.tgz"

echo "== install + restart =="
"${SSH[@]}" "$DEST" 'sudo bash -s' <<'EOF'
set -e
install -m 0755 /tmp/tr-signal /usr/local/bin/tr-signal
id trsignal >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin trsignal
rm -rf /opt/tr-web && mkdir -p /opt/tr-web
tar -C /opt/tr-web -xzf /tmp/tr-web.tgz
chmod -R a+rX /opt/tr-web
install -m 0644 /tmp/tr-signal.service /etc/systemd/system/tr-signal.service
systemctl daemon-reload
systemctl enable --now tr-signal
systemctl restart tr-signal
sleep 1
echo "active: $(systemctl is-active tr-signal)"
curl -s -o /dev/null -w "local healthz: %{http_code}\n" http://localhost/healthz
curl -s -o /dev/null -w "local SPA /: %{http_code}\n" http://localhost/
EOF
echo "done."

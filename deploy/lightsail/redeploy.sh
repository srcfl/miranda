#!/usr/bin/env bash
# Cross-compile mir-signal and (re)deploy it to the relay host, then restart the
# service. Idempotent: also (re)installs the systemd unit.
#
# Easy mode (uses the `tr-signal` SSH alias in ~/.ssh/config — the box is an AWS
# Lightsail instance still named `tr-signal`; the service it runs is `mir-signal`):
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
  DEST="${TARGET:-tr-signal}" # ~/.ssh/config alias for the Lightsail box
  SSH=(ssh -o ConnectTimeout=15)
  SCP=(scp -o ConnectTimeout=15)
fi

echo "== build mir-signal (linux/amd64, static) =="
( cd "$REPO/go" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/mir-signal-linux ./cmd/mir-signal )

echo "== package SPA (index.html + src + vendor) =="
tar -C "$REPO/web" -czf /tmp/mir-web.tgz index.html src vendor

echo "== upload to $DEST =="
"${SCP[@]}" /tmp/mir-signal-linux "$DEST:/tmp/mir-signal"
"${SCP[@]}" "$REPO/deploy/lightsail/mir-signal.service" "$DEST:/tmp/mir-signal.service"
"${SCP[@]}" /tmp/mir-web.tgz "$DEST:/tmp/mir-web.tgz"

echo "== install + restart =="
"${SSH[@]}" "$DEST" 'sudo bash -s' <<'EOF'
set -e
install -m 0755 /tmp/mir-signal /usr/local/bin/mir-signal
id mirsignal >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin mirsignal
rm -rf /opt/mir-web && mkdir -p /opt/mir-web
tar -C /opt/mir-web -xzf /tmp/mir-web.tgz
chmod -R a+rX /opt/mir-web
install -m 0644 /tmp/mir-signal.service /etc/systemd/system/mir-signal.service
systemctl daemon-reload
systemctl enable --now mir-signal
systemctl restart mir-signal
sleep 1
echo "active: $(systemctl is-active mir-signal)"
curl -s -o /dev/null -w "local healthz: %{http_code}\n" http://localhost/healthz
curl -s -o /dev/null -w "local SPA /: %{http_code}\n" http://localhost/
EOF
echo "done."

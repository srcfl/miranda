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

# Pin the exact bytes we just built. /tmp on the relay is world-writable, so
# without an integrity check a local user on the box (or a TOCTOU between scp and
# install) could swap the artifact that lands at root-owned /usr/local/bin. We
# verify this digest on the far end BEFORE installing anything as root.
sha256_of() { # macOS has shasum; Linux has sha256sum
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
SIG_SHA="$(sha256_of /tmp/mir-signal-linux)"
echo "   sha256(mir-signal)=$SIG_SHA"

echo "== package SPA (index.html + src + vendor) =="
tar -C "$REPO/web" -czf /tmp/mir-web.tgz index.html src vendor

echo "== upload to $DEST =="
"${SCP[@]}" /tmp/mir-signal-linux "$DEST:/tmp/mir-signal"
"${SCP[@]}" "$REPO/deploy/lightsail/mir-signal.service" "$DEST:/tmp/mir-signal.service"
"${SCP[@]}" /tmp/mir-web.tgz "$DEST:/tmp/mir-web.tgz"

echo "== install + restart =="
# Pass the expected digest as $1 so the remote script stays a quoted heredoc
# (no host-side interpolation of our local variables).
"${SSH[@]}" "$DEST" 'sudo bash -s' "$SIG_SHA" <<'EOF'
set -e
EXPECT_SHA="$1"
GOT_SHA="$(sha256sum /tmp/mir-signal | awk '{print $1}')"
if [ "$EXPECT_SHA" != "$GOT_SHA" ]; then
  echo "FATAL: mir-signal checksum mismatch — refusing to install" >&2
  echo "       expected $EXPECT_SHA" >&2
  echo "       got      $GOT_SHA" >&2
  exit 1
fi
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

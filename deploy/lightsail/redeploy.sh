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
# verify this digest on the far end BEFORE installing anything as root — and the
# remote heredoc closes the residual check->install race by moving the upload
# into a root-only staging dir first, then hashing *that* copy (#23).
sha256_of() { # macOS has shasum; Linux has sha256sum
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
SIG_SHA="$(sha256_of /tmp/mir-signal-linux)"
echo "   sha256(mir-signal)=$SIG_SHA"

echo "== package SPA (index.html + manifest + sw + icons + src + vendor) =="
# Strip macOS metadata: without COPYFILE_DISABLE + --no-xattrs the bsdtar on a
# Mac packs AppleDouble `._*` sidecar files (and xattr headers), which then land
# in /opt/mir-web and get served as bogus assets. --exclude drops any that exist.
# manifest.json/sw.js/icons are the PWA app-shell — sw.js MUST land at the webroot
# root (served at /sw.js) so its scope covers the whole origin.
COPYFILE_DISABLE=1 tar --no-xattrs --exclude='._*' \
  -C "$REPO/web" -czf /tmp/mir-web.tgz index.html manifest.json sw.js icons src vendor

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

# --- TOCTOU-safe staging (#23) ----------------------------------------------
# /tmp on the relay is world-writable, so a local user (or a race between scp
# and install) could swap /tmp/mir-signal after we hash it. Move it into a
# root-only staging dir FIRST, then hash and install from *that* verified copy —
# nothing unprivileged can touch it between the check and the install.
STAGE="$(mktemp -d /root/.mir-stage.XXXXXX)"
chmod 700 "$STAGE"
BACKUP="" # set later; cleaned by the same trap so nothing leaks under /root
trap 'rm -rf "$STAGE" ${BACKUP:+"$BACKUP"}' EXIT
mv /tmp/mir-signal "$STAGE/mir-signal"
mv /tmp/mir-signal.service "$STAGE/mir-signal.service"
mv /tmp/mir-web.tgz "$STAGE/mir-web.tgz"
GOT_SHA="$(sha256sum "$STAGE/mir-signal" | awk '{print $1}')"
if [ "$EXPECT_SHA" != "$GOT_SHA" ]; then
  echo "FATAL: mir-signal checksum mismatch — refusing to install" >&2
  echo "       expected $EXPECT_SHA" >&2
  echo "       got      $GOT_SHA" >&2
  exit 1
fi

# --- legacy tr-signal cutover (idempotent; no-op on future runs) -------------
# The live box first shipped as the pre-rename `tr-signal` setup. Tear that down
# BEFORE enabling mir-signal so the old unit can't fight the new one for :80.
# Every step is guarded / `rm -f` / `rm -rf`, so re-running is harmless.
if systemctl list-unit-files tr-signal.service >/dev/null 2>&1 \
   && systemctl cat tr-signal.service >/dev/null 2>&1; then
  systemctl disable --now tr-signal.service 2>/dev/null || true
fi
rm -f /etc/systemd/system/tr-signal.service
rm -rf /opt/tr-web
systemctl daemon-reload

# Migrate the TURN shared secret across the rename: TR_TURN_SECRET -> MIR_TURN_SECRET.
# Only when the new env file is absent and the old one exists (so we never clobber
# a freshly rotated secret on a re-run).
if [ ! -f /etc/mir-signal.env ] && [ -f /etc/tr-signal.env ]; then
  sed 's/^TR_TURN_SECRET=/MIR_TURN_SECRET=/' /etc/tr-signal.env > /etc/mir-signal.env
  chmod 600 /etc/mir-signal.env
  echo "migrated TURN secret: /etc/tr-signal.env -> /etc/mir-signal.env"
fi

id mirsignal >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin mirsignal

# --- back up current binary + unit so we can roll back on a failed health check -
BACKUP="$(mktemp -d /root/.mir-backup.XXXXXX)"
HAD_BIN=0; HAD_UNIT=0
if [ -f /usr/local/bin/mir-signal ]; then cp -p /usr/local/bin/mir-signal "$BACKUP/mir-signal"; HAD_BIN=1; fi
if [ -f /etc/systemd/system/mir-signal.service ]; then cp -p /etc/systemd/system/mir-signal.service "$BACKUP/mir-signal.service"; HAD_UNIT=1; fi

install -m 0755 "$STAGE/mir-signal" /usr/local/bin/mir-signal
rm -rf /opt/mir-web && mkdir -p /opt/mir-web
# Belt-and-suspenders: even if a `._*` file slipped into the tarball, drop it on
# extract so it never reaches the webroot.
tar -C /opt/mir-web --exclude='._*' -xzf "$STAGE/mir-web.tgz"
chmod -R a+rX /opt/mir-web
install -m 0644 "$STAGE/mir-signal.service" /etc/systemd/system/mir-signal.service
systemctl daemon-reload
systemctl enable --now mir-signal
systemctl restart mir-signal
sleep 1

# --- health-gated rollback ---------------------------------------------------
# Restart succeeded only if the unit is active AND localhost /healthz returns 200.
# Otherwise restore the backed-up binary+unit, reload, restart, and fail loudly
# so the deploy does not silently leave a broken relay live.
ACTIVE="$(systemctl is-active mir-signal || true)"
CODE="$(curl -s -o /dev/null -w '%{http_code}' http://localhost/healthz || true)"
echo "active: $ACTIVE"
echo "local healthz: $CODE"
curl -s -o /dev/null -w "local SPA /: %{http_code}\n" http://localhost/ || true
if [ "$ACTIVE" != "active" ] || [ "$CODE" != "200" ]; then
  echo "FATAL: mir-signal unhealthy after restart (active=$ACTIVE healthz=$CODE) — rolling back" >&2
  if [ "$HAD_BIN" = 1 ]; then install -m 0755 "$BACKUP/mir-signal" /usr/local/bin/mir-signal; fi
  if [ "$HAD_UNIT" = 1 ]; then install -m 0644 "$BACKUP/mir-signal.service" /etc/systemd/system/mir-signal.service; fi
  systemctl daemon-reload
  systemctl restart mir-signal || true
  RB_ACTIVE="$(systemctl is-active mir-signal || true)"
  RB_CODE="$(curl -s -o /dev/null -w '%{http_code}' http://localhost/healthz || true)"
  echo "rolled back (active=$RB_ACTIVE healthz=$RB_CODE)" >&2
  rm -rf "$BACKUP"
  exit 1
fi
rm -rf "$BACKUP"
echo "deploy healthy."
EOF
echo "done."

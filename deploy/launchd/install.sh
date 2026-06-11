#!/usr/bin/env bash
# Install a macOS LaunchAgent so `mir up` runs at login and restarts if it ever
# exits — your machine stays reachable without a terminal open. Idempotent.
#
#   ./deploy/launchd/install.sh            # install + load
#   ./deploy/launchd/install.sh uninstall  # remove
#
# IMPORTANT: this LaunchAgent owns the machine's registration slot on the relay.
# A hand-run `mir up` in a terminal registers the SAME owner_id + machine_id and
# competes with the LaunchAgent for that slot — the two fight, each kicking the
# other off (registration churn / a flapping agent). Pick one: either let this
# LaunchAgent serve the machine, or stop/uninstall it before running `mir up` by
# hand. (`launchctl bootout "$DOMAIN/$LABEL"`, or `./install.sh uninstall`.)
set -euo pipefail

LABEL="com.sourceful.terminal-relay-agent"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"

# Legacy LaunchAgent labels we must evict on install/uninstall so an upgrade does
# not leave a second, stale agent (KeepAlive=true) running and fighting the new
# one for the same registration slot. The current LABEL is kept stable so an
# in-place reinstall self-evicts; this list is for any historical names.
LEGACY_LABELS=()

bootout_label() { launchctl bootout "$DOMAIN/$1" 2>/dev/null || true; }

if [ "${1:-}" = "uninstall" ]; then
  bootout_label "$LABEL"
  for l in "${LEGACY_LABELS[@]:-}"; do [ -n "$l" ] && bootout_label "$l"; done
  rm -f "$PLIST"
  echo "uninstalled $LABEL"
  exit 0
fi

# Prefer the unified `mir` binary; `mir-agent` is a deprecated alias that only
# forwards to it (with a notice). Running the alias under launchd was the source
# of the "stale agent" incident, so resolve `mir` explicitly.
AGENT="$(command -v mir || echo "$HOME/.local/bin/mir")"
[ -x "$AGENT" ] || { echo "mir not found (run 'make install' first)"; exit 1; }
# The agent spawns tmux; make sure its directory is on PATH for the daemon.
TMUX_DIR="$(dirname "$(command -v tmux 2>/dev/null || echo /opt/homebrew/bin/tmux)")"
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array><string>$AGENT</string><string>up</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>$TMUX_DIR:/usr/local/bin:/usr/bin:/bin</string>
    <key>HOME</key><string>$HOME</string>
  </dict>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/terminal-relay-agent.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/terminal-relay-agent.log</string>
  <key>ProcessType</key><string>Background</string>
</dict></plist>
EOF

# Evict any prior instance of this label AND any legacy labels first, so an
# upgrade can't leave a second stale agent alive to fight the new one.
bootout_label "$LABEL"
for l in "${LEGACY_LABELS[@]:-}"; do [ -n "$l" ] && bootout_label "$l"; done
launchctl bootstrap "$DOMAIN" "$PLIST"
launchctl kickstart -k "$DOMAIN/$LABEL" 2>/dev/null || true
echo "installed + loaded $LABEL"
echo "  agent: $AGENT up   (logs: ~/Library/Logs/terminal-relay-agent.log)"
echo "  note: a hand-run 'mir up' competes with this LaunchAgent for the same"
echo "        registration slot — stop one before running the other."

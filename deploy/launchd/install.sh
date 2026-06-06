#!/usr/bin/env bash
# Install a macOS LaunchAgent so `tr-agent up` runs at login and restarts if it
# ever exits — your machine stays reachable without a terminal open. Idempotent.
#
#   ./deploy/launchd/install.sh            # install + load
#   ./deploy/launchd/install.sh uninstall  # remove
set -euo pipefail

LABEL="com.sourceful.terminal-relay-agent"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"

if [ "${1:-}" = "uninstall" ]; then
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  rm -f "$PLIST"
  echo "uninstalled $LABEL"
  exit 0
fi

AGENT="$(command -v tr-agent || echo "$HOME/.local/bin/tr-agent")"
[ -x "$AGENT" ] || { echo "tr-agent not found (run 'make install' first)"; exit 1; }
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

launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
launchctl bootstrap "$DOMAIN" "$PLIST"
launchctl kickstart -k "$DOMAIN/$LABEL" 2>/dev/null || true
echo "installed + loaded $LABEL"
echo "  agent: $AGENT up   (logs: ~/Library/Logs/terminal-relay-agent.log)"

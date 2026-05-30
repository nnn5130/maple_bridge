#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_PATH="${1:-configs/config.yaml}"
LOG_DIR="$ROOT_DIR/logs"
LOG_PATH="$LOG_DIR/maple_bridge.log"
ERR_PATH="$LOG_DIR/maple_bridge.err.log"
PID_PATTERN="./bin/maple_bridge $CONFIG_PATH"
LABEL="com.maple.maple_bridge"
PLIST_PATH="$HOME/Library/LaunchAgents/$LABEL.plist"
GUI_DOMAIN="gui/$(id -u)"

cd "$ROOT_DIR"

mkdir -p "$LOG_DIR"

echo "Building maple_bridge..."
go build -o bin/maple_bridge ./cmd/maple_bridge

echo "Stopping old launchd service if present..."
launchctl bootout "$GUI_DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true

echo "Stopping old maple_bridge processes..."
old_pids="$(pgrep -f "$PID_PATTERN|$ROOT_DIR/bin/maple_bridge $CONFIG_PATH" || true)"
if [[ -n "$old_pids" ]]; then
  echo "$old_pids" | xargs kill || true
  sleep 1

  old_pids="$(pgrep -f "$PID_PATTERN|$ROOT_DIR/bin/maple_bridge $CONFIG_PATH" || true)"
  if [[ -n "$old_pids" ]]; then
    echo "$old_pids" | xargs kill -9 || true
  fi
fi

cat >"$PLIST_PATH" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$ROOT_DIR/bin/maple_bridge</string>
    <string>$CONFIG_PATH</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$ROOT_DIR</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>StandardOutPath</key>
  <string>$LOG_PATH</string>
  <key>StandardErrorPath</key>
  <string>$ERR_PATH</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
PLIST

echo "Starting maple_bridge with launchctl..."
launchctl bootstrap "$GUI_DOMAIN" "$PLIST_PATH"
launchctl kickstart -k "$GUI_DOMAIN/$LABEL"

sleep 2
pid="$(pgrep -f "$ROOT_DIR/bin/maple_bridge $CONFIG_PATH" | head -n 1 || true)"
if [[ -n "$pid" ]] && ps -p "$pid" >/dev/null; then
  echo "Started maple_bridge pid=$pid via launchd label=$LABEL"
  echo "Log: $LOG_PATH"
  echo "Err: $ERR_PATH"
else
  echo "Failed to start maple_bridge. Last logs:"
  tail -n 80 "$LOG_PATH" || true
  tail -n 80 "$ERR_PATH" || true
  exit 1
fi

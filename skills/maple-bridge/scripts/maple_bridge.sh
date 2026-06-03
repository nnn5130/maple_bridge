#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${MAPLE_BRIDGE_REPO:-https://github.com/nnn5130/maple_bridge.git}"
INSTALL_DIR="${MAPLE_BRIDGE_HOME:-$HOME/.local/share/maple_bridge}"
CONFIG_PATH="${MAPLE_BRIDGE_CONFIG:-configs/config.yaml}"
LABEL="com.maple.maple_bridge"
GUI_DOMAIN="gui/$(id -u)"

usage() {
  cat <<'USAGE'
Usage: maple_bridge.sh <command>

Commands:
  install   Clone/update maple_bridge, build it, and create config.yaml if missing
  doctor    Check local prerequisites, config, binary, and service state
  build     Build bin/maple_bridge
  run       Run maple_bridge in the foreground
  restart   Build and restart the macOS launchd service
  status    Show launchd and process status
  logs      Show recent launchd stdout/stderr logs
  stop      Stop launchd service and matching maple_bridge processes
  path      Print the local maple_bridge checkout path
USAGE
}

have() {
  command -v "$1" >/dev/null 2>&1
}

require_cmd() {
  if ! have "$1"; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

repo_ready() {
  [[ -d "$INSTALL_DIR/.git" ]]
}

ensure_repo() {
  require_cmd git
  if repo_ready; then
    git -C "$INSTALL_DIR" fetch --prune
    git -C "$INSTALL_DIR" pull --ff-only
  else
    mkdir -p "$(dirname "$INSTALL_DIR")"
    git clone "$REPO_URL" "$INSTALL_DIR"
  fi
}

ensure_config() {
  cd "$INSTALL_DIR"
  if [[ ! -f "$CONFIG_PATH" ]]; then
    mkdir -p "$(dirname "$CONFIG_PATH")"
    cp configs/config.example.yaml "$CONFIG_PATH"
    echo "Created $INSTALL_DIR/$CONFIG_PATH"
    echo "Edit it with Feishu credentials, Codex path, working_dir, and user allowlists before restart."
  fi
}

build_bridge() {
  require_cmd go
  cd "$INSTALL_DIR"
  go build -o bin/maple_bridge ./cmd/maple_bridge
}

cmd_install() {
  require_cmd git
  require_cmd go
  if ! have codex; then
    echo "Warning: codex command was not found in PATH. Set codex.path in configs/config.yaml." >&2
  fi
  ensure_repo
  ensure_config
  build_bridge
  echo "Installed maple_bridge at $INSTALL_DIR"
  echo "Next: edit $INSTALL_DIR/$CONFIG_PATH, then run: $0 restart"
}

cmd_doctor() {
  echo "maple_bridge home: $INSTALL_DIR"
  for cmd in git go codex; do
    if have "$cmd"; then
      echo "ok: $cmd -> $(command -v "$cmd")"
    else
      echo "missing: $cmd"
    fi
  done

  if repo_ready; then
    echo "ok: repo present"
    git -C "$INSTALL_DIR" status --short || true
  else
    echo "missing: repo checkout"
  fi

  if [[ -f "$INSTALL_DIR/$CONFIG_PATH" ]]; then
    echo "ok: config $INSTALL_DIR/$CONFIG_PATH"
  else
    echo "missing: config $INSTALL_DIR/$CONFIG_PATH"
  fi

  if [[ -x "$INSTALL_DIR/bin/maple_bridge" ]]; then
    echo "ok: binary $INSTALL_DIR/bin/maple_bridge"
  else
    echo "missing: binary $INSTALL_DIR/bin/maple_bridge"
  fi

  cmd_status || true
}

cmd_restart() {
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "restart uses macOS launchd. Use '$0 run' for foreground mode on this OS." >&2
    exit 1
  fi
  ensure_repo
  ensure_config
  cd "$INSTALL_DIR"
  ./scripts/restart.sh "$CONFIG_PATH"
}

cmd_run() {
  cd "$INSTALL_DIR"
  exec ./bin/maple_bridge "$CONFIG_PATH"
}

cmd_status() {
  local plist="$HOME/Library/LaunchAgents/$LABEL.plist"
  if [[ -f "$plist" ]]; then
    echo "launchd plist: $plist"
    launchctl print "$GUI_DOMAIN/$LABEL" 2>/dev/null | sed -n '1,40p' || true
  else
    echo "launchd plist: missing"
  fi
  pgrep -fl "$INSTALL_DIR/bin/maple_bridge $CONFIG_PATH" || echo "process: not running"
}

cmd_logs() {
  local log_dir="$INSTALL_DIR/logs"
  echo "stdout: $log_dir/maple_bridge.log"
  tail -n "${MAPLE_BRIDGE_LOG_LINES:-120}" "$log_dir/maple_bridge.log" 2>/dev/null || true
  echo "stderr: $log_dir/maple_bridge.err.log"
  tail -n "${MAPLE_BRIDGE_LOG_LINES:-120}" "$log_dir/maple_bridge.err.log" 2>/dev/null || true
}

cmd_stop() {
  local plist="$HOME/Library/LaunchAgents/$LABEL.plist"
  launchctl bootout "$GUI_DOMAIN" "$plist" >/dev/null 2>&1 || true
  pgrep -f "$INSTALL_DIR/bin/maple_bridge $CONFIG_PATH" | xargs kill >/dev/null 2>&1 || true
  echo "Stopped maple_bridge if it was running."
}

main() {
  local command="${1:-}"
  case "$command" in
    install) cmd_install ;;
    doctor) cmd_doctor ;;
    build) ensure_repo; build_bridge ;;
    run) cmd_run ;;
    restart) cmd_restart ;;
    status) cmd_status ;;
    logs) cmd_logs ;;
    stop) cmd_stop ;;
    path) echo "$INSTALL_DIR" ;;
    ""|-h|--help|help) usage ;;
    *) echo "Unknown command: $command" >&2; usage >&2; exit 2 ;;
  esac
}

main "$@"

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
  configure Guide the user through writing configs/config.yaml
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
    echo "Run '$0 configure' or edit it with Feishu credentials, Codex path, working_dir, and user allowlists before restart."
  fi
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

strip_yaml_scalar() {
  local value="$1"
  value="$(trim "$value")"
  if [[ "$value" == \"* ]]; then
    value="${value#\"}"
    value="${value%%\"*}"
  else
    value="${value%%#*}"
    value="$(trim "$value")"
  fi
  printf '%s' "$value"
}

yaml_get_top_scalar() {
  local key="$1"
  local file="$INSTALL_DIR/$CONFIG_PATH"
  [[ -f "$file" ]] || return 0
  local raw
  raw="$(awk -v key="$key" '
    $0 ~ "^" key ":[[:space:]]*" {
      sub("^[^:]+:[[:space:]]*", "")
      print
      exit
    }
  ' "$file")"
  strip_yaml_scalar "$raw"
}

yaml_get_section_scalar() {
  local section="$1"
  local key="$2"
  local file="$INSTALL_DIR/$CONFIG_PATH"
  [[ -f "$file" ]] || return 0
  local raw
  raw="$(awk -v section="$section" -v key="$key" '
    $0 ~ "^[[:space:]]*" section ":[[:space:]]*$" {
      in_section = 1
      next
    }
    in_section && $0 ~ "^[^[:space:]][^:]*:" {
      in_section = 0
    }
    in_section {
      pattern = "^[[:space:]]*" key ":[[:space:]]*"
      if ($0 ~ pattern) {
        sub(pattern, "")
        print
        exit
      }
    }
  ' "$file")"
  strip_yaml_scalar "$raw"
}

yaml_get_top_list() {
  local key="$1"
  local file="$INSTALL_DIR/$CONFIG_PATH"
  [[ -f "$file" ]] || return 0
  local items=()
  local raw item
  while IFS= read -r raw; do
    item="$(strip_yaml_scalar "$raw")"
    [[ -n "$item" ]] && items+=("$item")
  done < <(awk -v key="$key" '
    $0 ~ "^" key ":[[:space:]]*$" {
      in_list = 1
      next
    }
    in_list && $0 ~ "^[^[:space:]][^:]*:" {
      exit
    }
    in_list && $0 ~ "^[[:space:]]*-[[:space:]]*" {
      sub("^[[:space:]]*-[[:space:]]*", "")
      print
    }
  ' "$file")
  local IFS=,
  printf '%s' "${items[*]}"
}

without_placeholder() {
  case "$1" in
    ""|"cli_xxxxxxxxxxxx"|"xxxxxxxxxxxxxxxxxxxx"|"/path/to/your/project"|"ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
      printf ''
      ;;
    *)
      printf '%s' "$1"
      ;;
  esac
}

prompt_value() {
  local label="$1"
  local current="$2"
  local mode="${3:-text}"
  local value
  if [[ "$mode" == "secret" ]]; then
    if [[ -n "$current" ]]; then
      read -r -s -p "$label [press Enter to keep current]: " value || true
    else
      read -r -s -p "$label: " value || true
    fi
    printf '\n' >&2
  else
    if [[ -n "$current" ]]; then
      read -r -p "$label [$current]: " value || true
    else
      read -r -p "$label: " value || true
    fi
  fi
  if [[ -z "$value" ]]; then
    value="$current"
  fi
  printf '%s' "$value"
}

normalize_bool() {
  local value
  value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    true|t|yes|y|1) printf 'true' ;;
    false|f|no|n|0) printf 'false' ;;
    *) return 1 ;;
  esac
}

yaml_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s' "$value"
}

write_yaml_list() {
  local csv="$1"
  local IFS=,
  local parts=()
  local part item wrote=false
  read -r -a parts <<< "$csv"
  for part in "${parts[@]}"; do
    item="$(trim "$part")"
    if [[ -n "$item" ]]; then
      printf '  - "%s"\n' "$(yaml_escape "$item")"
      wrote=true
    fi
  done
  if [[ "$wrote" == false ]]; then
    printf '  []\n'
  fi
}

cmd_configure() {
  ensure_repo
  ensure_config
  cd "$INSTALL_DIR"

  local app_id app_secret codex_path working_dir allow_all_users
  local allowed_users admin_users super_admin_users
  local default_app_id default_app_secret default_codex_path default_working_dir
  local default_allow_all_users default_allowed_users default_admin_users default_super_admin_users
  local default_max_idle_minutes default_log_level config_file config_dir tmp_file

  default_app_id="$(without_placeholder "$(yaml_get_section_scalar feishu app_id)")"
  default_app_secret="$(without_placeholder "$(yaml_get_section_scalar feishu app_secret)")"
  default_codex_path="$(without_placeholder "$(yaml_get_section_scalar codex path)")"
  if [[ -z "$default_codex_path" ]] && have codex; then
    default_codex_path="$(command -v codex)"
  fi
  default_working_dir="$(without_placeholder "$(yaml_get_top_scalar working_dir)")"
  default_allow_all_users="$(yaml_get_top_scalar allow_all_users)"
  default_allow_all_users="${default_allow_all_users:-false}"
  default_allowed_users="$(without_placeholder "$(yaml_get_top_list allowed_users)")"
  default_admin_users="$(without_placeholder "$(yaml_get_top_list admin_users)")"
  default_super_admin_users="$(without_placeholder "$(yaml_get_top_list super_admin_users)")"
  default_max_idle_minutes="$(yaml_get_section_scalar session max_idle_minutes)"
  default_max_idle_minutes="${default_max_idle_minutes:-30}"
  default_log_level="$(yaml_get_top_scalar log_level)"
  default_log_level="${default_log_level:-info}"

  echo "Configuring $INSTALL_DIR/$CONFIG_PATH"
  app_id="$(prompt_value "Feishu app_id" "$default_app_id")"
  app_secret="$(prompt_value "Feishu app_secret" "$default_app_secret" secret)"
  codex_path="$(prompt_value "Codex CLI path" "$default_codex_path")"
  working_dir="$(prompt_value "Codex working_dir" "$default_working_dir")"
  allow_all_users="$(prompt_value "Allow all Feishu users? true/false" "$default_allow_all_users")"
  if ! allow_all_users="$(normalize_bool "$allow_all_users")"; then
    echo "allow_all_users must be true or false." >&2
    exit 1
  fi

  allowed_users="$(prompt_value "Allowed Feishu open_ids, comma-separated" "$default_allowed_users")"
  if [[ -z "$default_admin_users" ]]; then
    default_admin_users="$allowed_users"
  fi
  admin_users="$(prompt_value "Admin Feishu open_ids, comma-separated" "$default_admin_users")"
  if [[ -z "$default_super_admin_users" ]]; then
    default_super_admin_users="$admin_users"
  fi
  super_admin_users="$(prompt_value "Super-admin Feishu open_ids, comma-separated" "$default_super_admin_users")"

  if [[ -z "$app_id" ]]; then
    echo "feishu.app_id is required." >&2
    exit 1
  fi
  if [[ -z "$app_secret" ]]; then
    echo "feishu.app_secret is required." >&2
    exit 1
  fi
  if [[ "$allow_all_users" == "false" && -z "$(trim "$allowed_users")" ]]; then
    echo "allowed_users is required unless allow_all_users is true." >&2
    exit 1
  fi
  if [[ -z "$working_dir" || ! -d "$working_dir" ]]; then
    echo "working_dir must be an existing directory." >&2
    exit 1
  fi
  if [[ -z "$codex_path" ]]; then
    echo "codex.path is required." >&2
    exit 1
  fi
  if ! command -v "$codex_path" >/dev/null 2>&1; then
    echo "Warning: codex path '$codex_path' was not found or is not executable." >&2
  fi

  config_file="$INSTALL_DIR/$CONFIG_PATH"
  config_dir="$(dirname "$config_file")"
  mkdir -p "$config_dir"
  tmp_file="$(mktemp "$config_dir/config.yaml.XXXXXX")"
  {
    printf '# maple_bridge configuration\n\n'
    printf 'feishu:\n'
    printf '  app_id: "%s"\n' "$(yaml_escape "$app_id")"
    printf '  app_secret: "%s"\n\n' "$(yaml_escape "$app_secret")"
    printf 'codex:\n'
    printf '  path: "%s"\n\n' "$(yaml_escape "$codex_path")"
    printf 'allow_all_users: %s\n\n' "$allow_all_users"
    printf 'allowed_users:\n'
    write_yaml_list "$allowed_users"
    printf '\nuser_names: {}\n\n'
    printf 'admin_users:\n'
    write_yaml_list "$admin_users"
    printf '\nsuper_admin_users:\n'
    write_yaml_list "$super_admin_users"
    printf '\nworking_dir: "%s"\n\n' "$(yaml_escape "$working_dir")"
    printf 'session:\n'
    printf '  max_idle_minutes: %s\n\n' "$default_max_idle_minutes"
    printf 'log_level: "%s"\n' "$(yaml_escape "$default_log_level")"
  } > "$tmp_file"
  chmod 600 "$tmp_file"
  mv "$tmp_file" "$config_file"
  echo "Wrote $config_file"
  echo "Next: run '$0 restart' on macOS, or '$0 run' for foreground mode."
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
  echo "Next: run '$0 configure', then '$0 restart' on macOS."
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
    configure) cmd_configure ;;
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

#!/usr/bin/env bash
set -euo pipefail

REPO_SLUG="${MAPLE_BRIDGE_REPO_SLUG:-nnn5130/maple_bridge}"
REF="${MAPLE_BRIDGE_REF:-master}"
SKILL_NAME="maple-bridge"
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
DEST="$CODEX_HOME/skills/$SKILL_NAME"
TMP_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

require_cmd curl
require_cmd tar

mkdir -p "$CODEX_HOME/skills"

ARCHIVE_URL="https://github.com/$REPO_SLUG/archive/refs/heads/$REF.tar.gz"
echo "Downloading $ARCHIVE_URL"
curl -fsSL "$ARCHIVE_URL" -o "$TMP_DIR/maple_bridge.tar.gz"
tar -xzf "$TMP_DIR/maple_bridge.tar.gz" -C "$TMP_DIR"

SRC="$(find "$TMP_DIR" -maxdepth 3 -type d -path "*/skills/$SKILL_NAME" | head -n 1)"
if [[ -z "$SRC" ]]; then
  echo "Could not find skills/$SKILL_NAME in archive." >&2
  exit 1
fi

if [[ -d "$DEST" ]]; then
  BACKUP="$DEST.backup.$(date +%Y%m%d%H%M%S)"
  mv "$DEST" "$BACKUP"
  echo "Existing skill moved to $BACKUP"
fi

cp -R "$SRC" "$DEST"
chmod +x "$DEST/scripts/maple_bridge.sh"

echo "Installed Codex skill: $DEST"
echo "Restart Codex to pick up the new skill."
echo "Then run: $DEST/scripts/maple_bridge.sh install"
echo "After install, run: $DEST/scripts/maple_bridge.sh configure"

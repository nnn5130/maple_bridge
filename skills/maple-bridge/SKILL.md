---
name: maple-bridge
description: Install, configure, run, restart, inspect, and troubleshoot maple_bridge, a self-hosted Feishu/Lark bot bridge that lets a user invoke the local Codex CLI from Feishu messages. Use when the user wants to set up the bridge, update it from GitHub, edit its config, manage its launchd service, check logs/status, or diagnose Feishu/Codex connectivity.
metadata:
  short-description: Operate the maple_bridge Feishu-to-Codex bridge
---

# maple_bridge

Use this skill to manage `maple_bridge` from GitHub:

`https://github.com/nnn5130/maple_bridge`

The bundled CLI is the preferred entrypoint:

```bash
~/.codex/skills/maple-bridge/scripts/maple_bridge.sh <command>
```

If the skill is installed somewhere else, resolve the script relative to this `SKILL.md`.

## Commands

- `install`: clone or update the repo into `$MAPLE_BRIDGE_HOME` or `~/.local/share/maple_bridge`, build the Go binary, and create `configs/config.yaml` from the example if missing.
- `configure`: interactively collect Feishu app credentials, Codex path, workspace path, and user allowlists, then write `configs/config.yaml`.
- `doctor`: verify `git`, `go`, `codex`, repo, config, binary, and launchd process state.
- `build`: build `bin/maple_bridge`.
- `run`: foreground run with `configs/config.yaml`.
- `restart`: build and restart the macOS LaunchAgent via the repo's `scripts/restart.sh`.
- `status`: show launchd and process status.
- `logs`: show recent stdout/stderr logs.
- `stop`: stop the launchd service and matching process.
- `path`: print the local repo path.

## Standard Workflow

1. Run `maple_bridge.sh doctor` first when diagnosing an existing installation.
2. Run `maple_bridge.sh install` for a new machine or to update/build the local checkout.
3. Run `maple_bridge.sh configure` to write `configs/config.yaml`. It prompts for:
   - `feishu.app_id`
   - `feishu.app_secret`
   - `codex.path`
   - `working_dir`
   - `allowed_users`
   - `admin_users`
   - `super_admin_users`
4. Run `maple_bridge.sh restart` on macOS to start the background service.
5. Run `maple_bridge.sh logs` if Feishu messages do not receive responses.

## Safety

Treat access to the Feishu bot as access to the local OS user running Codex. Keep `allow_all_users: false` unless the user explicitly accepts the risk. Never print `app_secret` or other credentials back to the user.

## Useful Feishu Bot Commands

- `/help`
- `/reload`
- `/reset`
- `/workspace`
- `/ll`
- `/cd <dir>`
- `/status`
- `/run <command>`
- `/start <command>`
- `/services`
- `/pid`
- `/logs <pid>`
- `/stop <pid>`

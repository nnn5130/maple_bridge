# maple_bridge

A personal self-hosted coding assistant that lets you control the Codex CLI on your own computer from Feishu/Lark.

## What It Does

```
Feishu message -> WebSocket event -> local Codex CLI -> Feishu response
```

The bridge reuses your local Codex CLI authentication and local filesystem access. Anyone who can use this bot effectively gets the ability to ask Codex and admin commands to operate on your computer as the current OS user.

## Quick Start

### Prerequisites

- Go 1.22+
- Codex CLI installed and logged in
- A Feishu/Lark custom app with bot capability and WebSocket event subscription enabled

### One-Click Codex Skill Install

Install the Codex skill if you want an AI agent to install, update, run, and troubleshoot the bridge for you:

```bash
curl -fsSL https://raw.githubusercontent.com/nnn5130/maple_bridge/master/scripts/install-codex-skill.sh | bash
```

Restart Codex, then ask:

```text
Use $maple-bridge to install and start the bridge.
```

You can also run the skill CLI directly:

```bash
~/.codex/skills/maple-bridge/scripts/maple_bridge.sh install
~/.codex/skills/maple-bridge/scripts/maple_bridge.sh doctor
~/.codex/skills/maple-bridge/scripts/maple_bridge.sh restart
```

The default project checkout is `~/.local/share/maple_bridge`. Override it with `MAPLE_BRIDGE_HOME=/path/to/maple_bridge`.

### Configure

```bash
cp configs/config.example.yaml configs/config.yaml
```

Edit `configs/config.yaml`:

- `feishu.app_id` and `feishu.app_secret`: your Feishu/Lark app credentials
- `working_dir`: the default workspace for Codex
- `allow_all_users`: keep this `false` for personal use
- `allowed_users`: Feishu/Lark user open IDs allowed to use the bot
- `admin_users`: users allowed to run shell commands and manage services
- `super_admin_users`: users allowed to manage all services and `cd` outside the workspace

### Run

Foreground:

```bash
make run
```

macOS LaunchAgent restart:

```bash
make restart
```

## Commands

- `/help`: show command help
- `/reload`: reload config from disk, super-admin only
- `/reset`: clear the current chat/topic Codex context
- `/workspace`: list the configured workspace root
- `/ll`: list the current chat/topic working directory
- `/cd <dir>`: switch the current chat/topic directory; super-admin can access all directories, other users stay inside `working_dir`
- `/status`: show session status
- `/run <command>`: run a one-shot shell command, admin only
- `/start <command>`: start a managed background service, admin only
- `/services`: list managed services
- `/pid`: list managed service PIDs
- `/logs <pid>`: show service logs
- `/stop <pid>`: stop a managed service, admin only

## Security Notes

- For personal use, put only your own open ID in `allowed_users`, `admin_users`, and `super_admin_users`.
- Avoid adding this bot to large group chats.
- Do not commit `configs/config.yaml`, local logs, Codex credentials, or `.maple_bridge/` runtime files.
- Admin commands run with the permissions of your local OS user.

## License

Apache License 2.0

# Contributing

This project is a personal self-hosted Feishu/Lark bridge for Codex CLI. Changes should preserve the default security posture: deny by default, local-first, and explicit admin access for shell execution.

By contributing, you agree that your contribution is licensed under the Apache License 2.0.

## Development

```bash
go test ./...
go vet ./...
```

## Pull Requests

- Do not commit `configs/config.yaml`, logs, local binaries, `.maple_bridge/`, `.claude/`, or credentials.
- Keep admin-only behavior for commands that execute shell commands or manage processes.
- Add or update tests for command parsing, permission checks, path restrictions, config validation, and service management behavior.

# Security Policy

`maple_bridge` is intended for personal self-hosted use. Treat access to the bot as access to your local computer under the current OS user.

## Recommended Configuration

- Keep `allow_all_users: false`.
- Put only trusted personal accounts in `allowed_users`.
- Put only the owner account in `admin_users` and `super_admin_users`.
- Do not commit `configs/config.yaml`, local logs, Codex credentials, or `.maple_bridge/` runtime data.
- Avoid adding the bot to large group chats.

## Reporting

If you find a security issue, open a private report through the repository host if available, or contact the maintainer directly. Do not publish secrets, app credentials, tokens, logs, or message contents in public issues.

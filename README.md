# maple_bridge

个人自托管代码助手 — 通过飞书 Bot 远程调用自己电脑上的 Codex CLI，完成编码、调试、启动本地服务等任务。

## 架构

```
飞书消息 → WebSocket 长连接 → Codex CLI → 结果返回飞书
```

直接复用本机 Codex CLI 的认证和能力。能使用这个 Bot 的飞书用户，基本等价于能以当前系统用户身份操作你的本机项目文件和命令行。

## 快速开始

### 1. 前置条件

- Go 1.22+
- Codex CLI 已安装并登录（`codex` 命令可用）
- 飞书自建应用（启用机器人能力，事件订阅选「长连接」模式）

### 2. 配置

```bash
cp configs/config.example.yaml configs/config.yaml
```

编辑 `configs/config.yaml`：
- `feishu.app_id` / `feishu.app_secret` — 飞书应用凭证
- `working_dir` — Codex 工作目录
- `allow_all_users` — 是否允许所有飞书用户使用，个人场景建议保持 `false`
- `allowed_users` — 允许使用的用户 open_id，`allow_all_users=false` 时必须配置
- `admin_users` — 管理员用户 open_id，可使用 shell 和后台服务命令
- `super_admin_users` — 超级管理员 open_id，可管理所有后台服务，且 `/cd` 可访问全部目录

### 3. 构建 & 运行

本地前台运行：

```bash
make run
```

macOS LaunchAgent 后台重启：

```bash
make restart
```

### 4. 飞书配置

在飞书开发者后台：
1. 创建自建应用，启用「机器人」能力
2. 事件订阅方式选择「使用长连接接收事件」
3. 添加事件：`im.message.receive_v1`

## 使用

在飞书中给 bot 发消息：

- `帮我看看当前目录下有什么文件`
- `写一个 Python 脚本计算斐波那契数列并运行`
- `运行 go test ./... 看看测试结果`
- `/help` — 查看内置命令
- `/reload` — 重新读取配置（仅超级管理员）
- `/reset` — 重置当前用户的运行状态
- `/workspace` — 查看配置的 workspace 根目录
- `/ll` — 查看当前用户的当前工作目录
- `/cd <dir>` — 切换当前用户的工作目录；super-admin 可访问全部目录，其他用户限制在 `working_dir` 内
- `/status` — 查看当前会话和工作目录
- `/run <command>` — 在当前工作目录执行一次性命令，最多运行 30 秒（仅管理员）
- `/start <command>` — 后台启动服务（仅管理员），日志写入 `.maple_bridge/logs/`
- `/services` — 查看由 bridge 管理的后台服务
- `/pid` — 查看由 bridge 管理的后台服务 PID
- `/logs <pid>` — 查看后台服务日志尾部
- `/stop <pid>` — 停止后台服务（仅管理员）

## 安全边界

- 建议只把自己的 `open_id` 放入 `allowed_users`、`admin_users` 和 `super_admin_users`
- 不建议把 Bot 放进大群或配置 `allow_all_users: true`
- 管理员消息会以本机当前用户权限执行 Codex、`/run` 和 `/start`
- 普通管理员和普通用户的 `/cd` 被限制在 `working_dir` 内，超级管理员不受限制
- Feishu `app_secret`、Codex 登录态、本地日志和 `.maple_bridge/` 运行数据不要提交到仓库

## 特性

- 用户状态：同一用户保留 30 分钟上下文、工作目录和轮次状态
- 长消息分段：超长输出自动拆分为多条飞书消息
- 用户白名单：默认要求显式配置允许访问的飞书用户
- 后台服务：管理员可用 `/start` 启动长期运行的服务，并通过 `/services`、`/pid`、`/logs`、`/stop` 管理
- 服务恢复：`/start` 启动的服务元数据持久化，bridge 重启后可继续管理仍在运行的服务
- 卡片反馈：Codex 处理时先发送状态卡片，完成或失败后更新同一张卡片
- 权限模型：普通管理员只能管理自己的服务，超级管理员可管理所有服务

## 项目结构

```
cmd/maple_bridge/main.go          # 入口
internal/
  config/config.go                 # 配置加载
  feishu/client.go                 # 飞书 WebSocket + 消息处理
  codex/runner.go                  # Codex CLI 调用 + 用户状态管理
configs/config.example.yaml
```

## License

Apache License 2.0

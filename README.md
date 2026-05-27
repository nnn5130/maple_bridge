# maple_bridge

AI Agent 桥接器 — 通过飞书 Bot 交互，直接驱动 Claude Code 完成编码与调试。

## 架构

```
飞书消息 → WebSocket 长连接 → Claude Code CLI → 结果返回飞书
```

无需 Anthropic API Key，直接复用本地 Claude Code 的认证和能力。

## 快速开始

### 1. 前置条件

- Go 1.22+
- Claude Code CLI 已安装并登录（`claude` 命令可用）
- 飞书自建应用（启用机器人能力，事件订阅选「长连接」模式）

### 2. 配置

```bash
cp configs/config.example.yaml configs/config.yaml
```

编辑 `configs/config.yaml`：
- `feishu.app_id` / `feishu.app_secret` — 飞书应用凭证
- `working_dir` — Claude Code 工作目录
- `allowed_users` — 允许使用的用户 open_id（留空允许所有）

### 3. 构建 & 运行

```bash
make build
./bin/maple_bridge configs/config.yaml
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
- `/reset` — 重置对话（开启新的 Claude Code 会话）

## 特性

- 多轮对话：同一用户自动保持 Claude Code 会话上下文
- 会话过期：空闲 60 分钟后自动清理
- 长消息分段：超长输出自动拆分为多条飞书消息
- 用户白名单：可限制访问权限

## 项目结构

```
cmd/maple_bridge/main.go          # 入口
internal/
  config/config.go                 # 配置加载
  feishu/client.go                 # 飞书 WebSocket + 消息处理
  claude/runner.go                 # Claude Code CLI 调用 + 会话管理
configs/config.example.yaml
```

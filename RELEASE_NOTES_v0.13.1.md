# Coconet 0.13.1

CLI output now starts with a clear result, followed by relevant status and next steps. Project initialization avoids duplicate success messages, and status distinguishes the local upload queue from server-side processing. Invitations display a local expiry time and time zone, fixing the hourly rounding that could show slightly more than 60 minutes as two hours.

Agent setup explicitly describes the Coconet plugin and groups configured commands. Setup retry commands preserve each instance's command and configuration directory. Help, account and session actions, Session Library browsing, graph diagnostics, and uninstall guidance use clearer wording and layout. Interactive output has light styling; redirected output, NO_COLOR and TERM=dumb remain plain text. Existing JSON, account-key and executable-command outputs retain their contracts.

## Upgrade

```bash
npm install --global coconet@latest
coconet version
```

This release updates Client, npm and both Agent plugins to 0.13.1. Server remains 0.13.0; no Hosted deployment, database migration or operator upgrade is required. New Connection Codes still expire in one hour and have no per-code use quota.

## Validation

Full engineering checks passed, including command recovery boundaries and isolated Runtime/Server terminal checks. macOS arm64 and Linux amd64 installation checks use isolated mock Agent hosts; no real Agent/model interaction is claimed. Other platforms receive build and applicable signing/notarization verification.

## 中文说明

CLI 先显示明确的操作结果，再提供状态和下一步。初始化减少重复输出；邀请显示带时区的本地到期时间，修复按小时向上取整可能把一小时显示成两小时的问题。空上传队列不再容易被误读为所有云端处理均已完成。

Agent 安装说明明确指向 Coconet 插件，重试命令保留自定义实例参数。帮助、账号 / 会话操作、Session Library、图诊断与卸载文案同步整理。终端提供轻量样式，重定向和 NO_COLOR / TERM=dumb 保持纯文本。

Client / npm / 双插件更新为 0.13.1，Server 与 Hosted 保持 0.13.0；本次无需迁移数据库或更新服务端。

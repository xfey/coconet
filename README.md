# Coconet

Coconet helps teams understand shared Coding Agent work through a DAG, read selected conversations locally, and continue work in a new Session with the same Agent type.

Coconet 帮助团队通过 DAG 理解 Coding Agent 的工作进度与依赖，按需拉取会话到本地阅读、检索，并使用同类型 Agent 接力。

This repository distributes release documentation and binaries. Implementation source is not published here yet; GitHub's automatically generated source archives contain only these public documents.

当前仓库仅分发文档与制品，尚未公开实现源码；GitHub 自动生成的源码压缩包仅包含公开文档。

## 0.20.1: automatic local inference / 自动本地推理

Client, Server, npm and both Plugins use version **0.20.1**. See [release notes](RELEASE_NOTES_v0.20.1.md).

- Your device captures new conversation updates and runs two local model stages: work-stage decisions, then dependency decisions. Codex conversations use the source installation’s app-server; Claude conversations use its print mode. Both inherit the Agent’s login and default model, with no separate inference setup or call quota.
- The Relay handles accounts, project permissions, ordered commits, retained content and published graph checkpoints. It does not run models.
- The Dashboard shows the last published DAG even when producing devices are offline. It refreshes after published updates. It provides fixed-source commands for local reading, Library operations and same-Agent Fork.
- Discover work through DAG / Library metadata, then pull the selected compact conversation for local reading and search. Original artifacts are fetched explicitly for native continuation.
- Current and referenced snapshots are retained; eligible unreferenced versions can be reclaimed after reference checks and protection periods. Verified cached conversations remain readable while the Relay is unavailable. Local processing archives support diagnosis and deterministic replay.

设备捕获新对话更新，本地模型分两轮判断工作阶段与依赖；Relay 负责权限、持久化和分发，不调用模型。网页读取最近发布的图，即使生产设备离线仍可查看。先从 DAG 或 Library 找到工作，再按需拉取精简会话；明确接力时才拉取原始 Session。当前及被引用的快照保留，符合条件的无引用版本逐步回收，处理档案与日志供排查和重放。

**This MVP is not end-to-end encrypted.** Server administrators can read uploaded payloads, and inference providers receive the selected model input. Encryption, old-DAG migration and ultra-long-conversation segmentation are deferred. Project content and commit-log quotas do not replace whole-server disk monitoring.

**本 MVP 尚未端到端加密。** 服务器管理员仍可读取上传内容，推理提供方会收到选定的模型输入。加密、旧 DAG 迁移与超长会话分段后置；项目配额不能替代整机容量监控。

## Install and connect / 安装与连接

Supported: macOS Apple Silicon / Intel and Linux arm64 / x86_64. Windows is not supported.

```sh
npm install --global coconet@0.20.1
coconet version
```

The npm launcher downloads only your platform's matching release, verifies its pinned SHA-256 and installs the Runtime. npm's global bin directory must be on PATH. Missing Agents are skipped; failed Agent setup does not undo Runtime installation. Inspect `coconet agent list --verbose` for results and retry commands. Start a new Agent session to load the Plugin and approve Hook trust when prompted.

npm 启动器按系统下载对应制品并核对固定 SHA-256，自动配置已安装的默认 Codex / Claude。某个 Agent 配置失败不影响 Runtime 安装，可用 `coconet agent list --verbose` 查看与重试。升级后开启新的 Agent 会话加载插件。

Run inside your work directory / 在工作目录内执行：

```sh
coconet connect
coconet inference status
```

Coconet automatically uses the Agent installation that produced each new conversation, with its existing login and default model. No separate inference login, model selection or daily call limit is required. Background processing uses that Agent provider’s normal billing. Existing history is not automatically imported.

Coconet 自动使用产生该会话的 Agent 安装、现有登录和默认模型，无需另行配置推理账号、模型或每日调用上限。后台处理按 Agent 提供方的正常规则计费。连接目录不会自动导入旧历史。

```sh
coconet inference pause
coconet inference resume
coconet status
coconet health --json
coconet diagnostics list
```

The browser confirms the account, device and directory before connecting to an existing or new project. SSH users can run `coconet connect --no-browser` and open the printed link on their own computer. Use `--path /path/to/folder` to select another directory. Invitations are available from the Dashboard.

网页会确认账号、设备和目录，然后选择已有项目或新建项目。SSH 用户可用 `--no-browser`，在自己的电脑打开终端链接；`--path` 指定远程目录，分享邀请可在网页获取。

Hosted Dashboard: <https://coconet.space/>. Hosted API: `https://api.coconet.space`.

## Upgrade / 升级

Install 0.20.1 and start a new Agent session. From 0.20.0, separate inference configuration is no longer used; `coconet inference status` reports automatic mode. Pausing and resuming remain available. Source Agent authentication and default model settings stay under the Agent’s control.

升级到 0.20.1 后开启新 Agent 会话。旧的独立推理配置不再使用，`coconet inference status` 显示自动模式；仍可暂停和恢复。登录状态与默认模型由来源 Agent 自身管理。

When upgrading from 0.19 or reconnecting after a development-project reset, run `coconet connect` in the work directory. Old server-generated DAGs are not migrated. Native Agent history is preserved; historical import starts with `coconet history preview --agent codex` or `claude`, then an explicit selection.

从 0.19 升级或开发项目重置后，在工作目录重新执行 `coconet connect`。旧服务端 DAG 不迁移，原生 Agent 历史保留；如需导入历史，先预览再明确选择。

## Additional Agent instances / 额外 Agent 实例

```sh
coconet agent add codex --command tcodex --config-root ~/.tcodex
coconet agent add claude --command tclaude --config-root ~/.tclaude
coconet agent list --verbose
```

Normal setup respects `CODEX_HOME` / `CLAUDE_CONFIG_DIR` and otherwise uses the Agent’s default directory. Register extra installations with the commands above. Coconet resolves inference from each captured conversation’s Agent type and configuration directory; ambiguous or unavailable installations produce a retryable diagnostic. `COCONET_SKIP_AGENT_SETUP=1` installs only the Runtime.

常规安装遵循 Agent 配置目录环境变量，额外安装通过上述命令登记。每段会话按来源 Agent 类型和配置目录确定后台调用；安装缺失或存在歧义时会记录可重试错误，不自动切换账号。首次运行设置 `COCONET_SKIP_AGENT_SETUP=1` 可只安装 Runtime。

## Self-hosted Relay / 自部署

Linux Server operator bundles are distributed separately in the same Release; npm does not install the Server. Each bundle includes systemd, Nginx and environment configuration examples. Place the service behind HTTPS and configure the chosen account authentication method before exposing it publicly.

The default account database is local bbolt. Relay stores commits, checkpoints, content objects, receipts and retention metadata in `<data_path>.relay`; `COCONET_RELAY_HOME` can select another absolute path. New Relay content uses the local filesystem, not the legacy S3 artifact backend. PostgreSQL authority storage requires an explicit Relay path; purchasing an external database is not required for this MVP.

Linux Server 在同一 Release 单独分发，制品内含 systemd、Nginx 和环境变量示例。默认账号库为本机 bbolt，新 Relay 数据保存在本机文件系统；无需购买外部数据库。旧 S3 配置不承接新 Relay 内容。公网部署需配置 HTTPS、身份认证并监控磁盘容量。

Stop the service before a complete backup. Keep the database, object directories and completion manifest together and private; the manifest includes Deployment identity. Restore into a new directory before switching service configuration. External OAuth secrets, TLS and proxy configuration require separate backups.

```sh
coconet-server backup --config /srv/coconet/server.json --output /safe/backup/server.db
coconet-server restore --input /safe/backup/server.db --data-dir /srv/coconet-restored
```

完整备份必须停服，账号库、对象目录和完成清单一起保管；清单含 Deployment 身份，不能公开。恢复到新目录后再切换服务；OAuth 密钥、TLS 和代理配置需独立备份。

## Disconnect and uninstall / 断开与卸载

`coconet disconnect` removes only the current directory association. `coconet logout` signs out this device. Neither deletes native conversation history.

```sh
coconet uninstall
npm uninstall --global coconet
```

Run Runtime uninstall before removing npm so it can remove managed Plugins and Hooks. Local Coconet state is retained unless `--purge` is explicitly selected. Restart existing Agent sessions after uninstalling.

先运行 Runtime 卸载以清理受管理的插件和 Hook，再移除 npm 包。默认保留 Coconet 本地状态，只有显式 `--purge` 才清理；卸载后重启已有 Agent 会话。

# Coconet

Coconet helps teams understand shared Coding Agent work through a DAG, read selected conversations locally, and continue work in a new Codex or Claude Session, including cross-Agent continuation.

Coconet 帮助团队通过 DAG 理解 Coding Agent 的工作进度与依赖，按需拉取会话到本地阅读、检索，并在 Codex 与 Claude 之间接力。

This repository distributes release documentation and binaries. Implementation source is not published here yet; GitHub's automatically generated source archives contain only these public documents.

当前仓库仅分发文档与制品，尚未公开实现源码；GitHub 自动生成的源码压缩包仅包含公开文档。

## 0.27.0: cross-Agent continuation / 跨 Agent 接力

Client, npm and both Plugins are **0.27.0**. Codex ↔ Claude Fork automatically converts effective main dialogue on the receiving device and returns a new Session ID and resume command. It does not duplicate existing DAG work. Tools, reasoning, images and workspace files are not transferred. See [release notes](RELEASE_NOTES_v0.27.0.md).

客户端、npm 与双 Agent 插件已更新为 **0.27.0**。跨 Agent 接力自动在用户设备转换有效主对话，返回新会话 ID 和继续命令；转换本身不重复生成 DAG 工作节点，不迁移工具状态、推理、图片或工作区文件。

Server **0.27.0** artifacts are available for self-hosting. Hosted remains **0.26.5** and is compatible with this client release; no data migration is required.

自部署提供 **0.27.0** 服务端制品；Hosted 保持 **0.26.5**，可兼容新版客户端，无需数据迁移。

A Project ID grants joining access to a signed-in user: share it only with intended collaborators. Opening a link alone does not join the project. Upgrade participating clients to use cross-Agent continuation.

已登录用户可通过项目 ID 加入项目，请只分享给希望加入的协作者。仅打开链接不会自动加入。需要跨 Agent 接力的设备请升级客户端。

**This MVP is not end-to-end encrypted.** Server administrators can read uploaded payloads, and inference providers receive the selected model input. Encryption, old-DAG migration and ultra-long-conversation segmentation are deferred. Project content and commit-log quotas do not replace whole-server disk monitoring.

**本 MVP 尚未端到端加密。** 服务器管理员仍可读取上传内容，推理提供方会收到选定的模型输入。加密、旧 DAG 迁移与超长会话分段后置；项目配额不能替代整机容量监控。

## Install and connect / 安装与连接

Supported: macOS Apple Silicon / Intel and Linux arm64 / x86_64. Windows is not supported.

```sh
npm install -g coconet@0.27.0
coconet version
```

The npm launcher downloads only your platform's matching release, verifies its pinned SHA-256 and installs the Runtime. npm's global bin directory must be on PATH. Missing Agents are skipped; failed Agent setup does not undo Runtime installation. Inspect `coconet agent list --verbose` for results and retry commands. Start a new Agent session to load the Plugin and approve Hook trust when prompted.

npm 启动器按系统下载对应制品并核对固定 SHA-256，自动配置已安装的默认 Codex / Claude。某个 Agent 配置失败不影响 Runtime 安装，可用 `coconet agent list --verbose` 查看与重试。升级后开启新的 Agent 会话加载插件。

Run inside your work directory / 在工作目录内执行：

```sh
coconet connect
coconet inference status
```

Coconet automatically uses the Agent installation that produced each new conversation, with its existing login and default model. No separate inference login, model selection or daily call limit is required. Background processing uses the executing Agent provider’s normal billing, including tasks taken over from other devices. Existing history is not automatically imported.

Coconet 自动使用产生该会话的 Agent 安装、现有登录和默认模型，无需另行配置推理账号、模型或每日调用上限。后台处理（包括接管其他设备的任务）按执行方 Agent 提供方的正常规则计费。连接目录不会自动导入旧历史。

```sh
coconet inference pause
coconet inference resume
coconet status
coconet health --json
coconet diagnostics list
```

Pausing stops inference on this device only; already shared tasks may still be handled by other members.

暂停只停止本设备推理，已共享的任务仍可由其他成员设备接手。

The browser confirms the account, device and directory before connecting to an existing or new project. SSH users can run `coconet connect --no-browser` and open the printed link on their own computer. Use `--path /path/to/folder` to select another directory. Share the Project ID or project link from the Dashboard. `coconet connect PROJECT_ID` preselects the target project; `--server https://your-server.example` selects another deployment.

网页会确认账号、设备和目录，然后选择已有项目或新建项目。SSH 用户可用 `--no-browser`，在自己的电脑打开终端链接；`--path` 指定远程目录，在网页分享项目 ID 或链接。`coconet connect PROJECT_ID` 可预选目标项目，`--server https://your-server.example` 可指定其他部署。

Hosted Dashboard: <https://coconet.space/>. Hosted API: `https://api.coconet.space`.

## Upgrade / 升级

Upgrade collaborating clients to 0.27.0, then start new Agent sessions to load the updated Plugins and tool descriptions. Existing connections, projects and native histories remain in place. The current 0.26.5 Relay is compatible; upgrading a self-hosted Server is optional.

将协作客户端升级到 0.27.0，再开启新的 Agent 会话加载插件和工具说明。现有目录连接、项目与原生历史保留；当前 0.26.5 Relay 兼容新版客户端，自部署服务器可按需升级。

Use `coconet --help`, `coconet help connect` or `coconet-server --help` for command usage. See the [parameter manual](docs/cli.md) and [MCP tools](docs/mcp-tools.md) for details. Search pages allow 1–20 results (default 10); use `next_cursor` for more results.

## Additional Agent instances / 额外 Agent 实例

```sh
coconet agent add claude --command tclaude --config-root ~/.tclaude
coconet agent list --verbose
```

Normal setup respects `CODEX_HOME` / `CLAUDE_CONFIG_DIR` and otherwise uses the Agent’s default directory. Register extra installations with the commands above. Coconet resolves inference from each captured conversation’s Agent type and configuration directory; ambiguous or unavailable installations produce a retryable diagnostic. `COCONET_SKIP_AGENT_SETUP=1` installs only the Runtime.

常规安装遵循 Agent 配置目录环境变量，额外安装通过上述命令登记。每段会话按来源 Agent 类型和配置目录确定后台调用；安装缺失或存在歧义时会记录可重试错误，不自动切换账号。首次运行设置 `COCONET_SKIP_AGENT_SETUP=1` 可只安装 Runtime。

## Self-hosted Relay / 自部署

Linux Server operator bundles are distributed separately in the same Release; npm does not install the Server. Each bundle includes systemd, scoped journal retention, Nginx and environment configuration examples. Place the service behind HTTPS and configure the chosen account authentication method before exposing it publicly.

The default account database is local bbolt. Relay stores commits, checkpoints, content objects, receipts and retention metadata in `<data_path>.relay`; `COCONET_RELAY_HOME` can select another absolute path. Filesystem content remains available for development. Set the independent COCONET_RELAY_S3_* variables to use direct object-store transfer and application-managed expiry; legacy S3 variables are not inherited automatically. An initialized Relay directory cannot change backend identity in place. PostgreSQL authority storage requires an explicit Relay path; purchasing an external database is not required for this MVP.

Linux Server 在同一 Release 单独分发，制品内含 systemd、Nginx 和环境变量示例。默认账号库为本机 bbolt，无需购买外部数据库。文件内容后端可用于开发；临时对象存储使用独立 COCONET_RELAY_S3_* 配置，不能直接替换已初始化 Relay 的后端。公网部署需配置 HTTPS、身份认证并监控磁盘容量。

The operator bundle includes storage monitoring and backup scripts under `examples/operations/`. For bbolt with external Relay content and no legacy artifact inventory, a private Unix socket supports consistent online metadata snapshots with independent restore verification. File-content deployments still require stopping the service for a complete backup. See `STORAGE.md` in the bundle for installation, budgets and retention. Keep the database, object directories and completion manifest together and private; the manifest includes Deployment identity. Restore into a new directory before switching service configuration. For an external content backend this server backup contains metadata and temporary task inputs, not Session bodies. Use `coconet storage export --file /absolute/project.tar` on a device to archive retained project content. Temporary object storage is not a permanent backup. External OAuth secrets, TLS and proxy configuration require separate backups.

```sh
coconet-server backup --config /srv/coconet/server.json --output /safe/backup/server.db
coconet-server restore --input /safe/backup/server.db --data-dir /srv/coconet-restored
```

制品的 `examples/operations/` 提供巡检、备份脚本与 `STORAGE.md` 部署说明。bbolt + 外部 Relay 内容且无旧 artifact 库存时，可通过私有 Unix socket 在线备份并独立恢复校验；文件正文后端仍需停服完整备份。同机备份不能抵御整机丢盘。账号库、对象目录和完成清单一起保管；清单含 Deployment 身份，不能公开。恢复到新目录后再切换服务；外部内容后端的服务端备份保存元数据引用和临时任务输入，项目正文应在设备上使用 `coconet storage export --file /absolute/project.tar` 归档。临时对象存储不是永久备份；OAuth 密钥、TLS 和代理配置需独立备份。

## Disconnect and uninstall / 断开与卸载

`coconet disconnect` removes only the current directory association. `coconet logout` signs out this device. Neither deletes native conversation history.

```sh
coconet uninstall
npm uninstall -g coconet
```

Run Runtime uninstall before removing npm so it can remove managed Plugins and Hooks. Local Coconet state is retained unless `--purge` is explicitly selected. Restart existing Agent sessions after uninstalling.

先运行 Runtime 卸载以清理受管理的插件和 Hook，再移除 npm 包。默认保留 Coconet 本地状态，只有显式 `--purge` 才清理；卸载后重启已有 Agent 会话。

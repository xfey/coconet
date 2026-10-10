# Coconet 0.20.1

Client, Server, npm and both Agent Plugins are updated together.

- New conversations automatically use their source Codex or Claude installation, existing login and default model for local DAG generation. Separate inference login/configuration and daily attempt quotas are removed; status, pause and resume remain.
- Temporary Debug login can be enabled by a server operator with `COCONET_DEBUG_LOGIN=true` (default off). D01–D05 are ordinary, distinct test accounts entered by ID. They support project invitations and normal device/folder connection. Disable the flag after testing; it invalidates Debug browser sessions, device tokens and unredeemed approvals. ID-only login is a development feature.
- Connection output groups device, folder and code, and the Dashboard explains processing progress more plainly in English and Chinese.
- Fixed-source reads verify available local content before contacting the Relay. Cached compact and native content remain readable during an outage; missing bytes still require a download and corrupted content remains an error.
- Automatic inference preserves source routing, actual reported models, usage, inputs, outputs and retry diagnostics without inserting routing metadata into model prompts.

The release was tested with a real p-limit workspace on macOS TClaude and Linux TCodex under two Debug accounts. Four natural conversation turns produced two work nodes and one adoption dependency; minor refinement kept the original node, inspection alone created no node, and both replicas and deterministic replays agreed. Restart, offline reads, browser flows and automated/race checks passed. This short sample is not a claim of arbitrary project scale.

## 中文

- 新对话自动使用来源 Codex / Claude 的现有登录和默认模型生成 DAG，无需额外推理配置或每日额度；保留查看状态、暂停和恢复。
- 可选的临时 Debug 登录提供 D01–D05 五个普通测试账号，支持邀请和目录绑定，默认关闭；关闭开关后对应会话、设备凭据和未兑换授权失效。
- 简化连接信息与中英文运行状态说明。
- 已校验的本地会话可在 Relay 不可用时读取，缺失内容才联网；损坏内容继续明确报错。
- 本机与开发机通过四轮真实主对话、自动 Hook、构图、跨账号读取与协作、重启及离线验收，全量自动化与并发检查通过。

Install with `npm install --global coconet@0.20.1`, then run `coconet connect` and start a new Agent session. There is no need to delete native Agent history. The Relay remains unencrypted in this MVP and does not run inference models. Implementation source is not included in this public distribution repository.

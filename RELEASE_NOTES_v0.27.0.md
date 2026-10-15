# Coconet 0.27.0 — cross-Agent continuation / 跨 Agent 接力

Codex and Claude can now continue each other's selected Coconet Sessions. Call the existing `fork_session` tool from the receiving Agent; Coconet selects the appropriate native Fork or text-conversion path locally and returns a new native Session ID and resume command.

Codex 与 Claude 现在可以接力彼此的固定会话。在接收端 Agent 调用原有 `fork_session` 即可，本机 Coconet 自动选择原生 Fork 或主对话转换，返回新会话 ID 和继续命令，无需额外转换步骤。

- Conversion runs on the receiving device, without a model call. The Relay transfers missing content and synchronizes metadata; it does not run the conversion.
- Same-Agent Fork preserves native history. Cross-Agent Fork preserves effective user/assistant text; tools, reasoning, images and working-tree files are not migrated.
- Imported history becomes an inherited baseline without duplicating DAG work. New conversation follows the normal work-stage and relationship flow.
- Fixed local copies support offline creation. Repeated requests reuse the same child identity; baseline publication and new observations recover after reconnection.

转换在用户设备执行，不调用模型；Relay 不执行转换。跨 Agent 只继承有效主对话，不迁移工具状态、推理、图片或工作区文件。导入本身不新增 DAG 工作节点，后续实际进展正常处理。本地副本完整时可离线创建，恢复联网后自动同步。

## Compatibility / 兼容范围

Validated with Codex 0.162.1, Claude Agent SDK 0.3.241, and interactive TClaude / Claude Code 2.1.263. The receiving device needs only its target Agent. The current conversation is not switched automatically: enter the new Session with the returned resume command.

Codex's importer cannot preserve leading assistant messages or text-edge whitespace exactly. These inputs are rejected with `pull_session` guidance, rather than silently altered. Claude-target conversion accepts these forms.

已验证上述原生版本；目标设备只需安装接收端 Agent。当前对话不会自动切换，请使用返回的命令进入新会话。Codex 官方导入器无法完整保留部分特殊文本时，会明确说明并提示改用读取会话。

## Install / 安装

```sh
npm install -g coconet@0.27.0
coconet version
```

Restart the Agent to load updated Plugin and tool descriptions. Existing projects, connections and original histories are preserved. Client 0.27.0 works with the existing 0.26.5 Relay; no server data migration is required. Server 0.27.0 artifacts also include updated Dashboard continuation instructions. Hosted remains 0.26.5 in this client release.

升级后重新打开 Agent 加载新插件和工具说明。已有项目、连接及原始历史保留；客户端可连接现有 0.26.5 Relay，无需服务端数据迁移。另提供含新版 Dashboard 接力说明的 0.27.0 服务端制品；本次客户端发布不更新 Hosted。

## Validation / 验收

- Complete engineering checks and targeted race checks passed.
- 23 native import cases, including 2,000 messages and a roughly 2 MB message, passed exact-text verification.
- macOS TClaude ↔ Linux Codex interactive continuation, fresh offline creation, reconnect publication, local Search / fixed Read and idempotent retry passed.
- Two isolated project branches each passed 34 project tests. Original user conversations were untouched.

The npm package remains a small verified launcher; platform archives contain the Runtime and bundled helpers. This repository distributes documents and release assets, not implementation source.

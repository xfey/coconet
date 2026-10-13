# Coconet 0.24.0

Native conversation rewind now updates the shared work graph. When a supported Codex Revert or Claude Code rewind replaces the conversation suffix, Coconet withdraws affected work and dependency edges, preserves valid earlier work and colleagues’ independent nodes, and rebuilds a node when its combined summary crosses the rewind boundary. Late processing results cannot restore the withdrawn branch.

原生会话回退现在会同步到工作图。Codex Revert 或 Claude Code 回退替换后续对话后，Coconet 撤下失效工作及关联边，保留有效的前序工作和同事独立节点；合并摘要跨越回退边界时重建该节点。迟到的处理结果不会恢复已撤回分支。

Default search, fixed-source reads and replica synchronization use the active branch. An explicit saved Library item may retain withdrawn historical evidence; exported files and existing independent Forks are unchanged. Coconet follows the Agent’s effective conversation and does not undo code changes. An ordinary independent Fork is not treated as a parent rewind.

默认检索、固定来源读取和副本同步使用有效分支。显式收藏可保留失效历史证据；已导出文件和已建立的独立 Fork 不被远程改写。Coconet 跟随 Agent 的有效对话，不代替 Agent 撤销代码；普通独立 Fork 不会撤回父会话。

TCodex-specific compatibility has been removed. Original Codex App Server / Plugin and Claude Code / TClaude integration remain supported. This release also includes the simplified Dashboard toolbar previously deployed in Hosted 0.23.2.

移除 TCodex 专用兼容，保留原版 Codex App Server / Plugin 与 Claude Code / TClaude 接入；同时包含此前 Hosted 0.23.2 的 Dashboard 工具栏整理。

Upgrade the Server first, then every collaborating client to 0.24.0. Earlier clients cannot read the new branch-reconciliation commits and checkpoints. Accounts, projects, deployment identity and folder connections are preserved. This public repository contains distribution documentation and artifacts only.

先升级服务器，再将全部协作客户端升级到 0.24.0。旧客户端无法读取新的分支协调提交和检查点；账号、项目、部署身份及目录关联保留。公共仓库仅分发文档和制品。

Validation includes full engineering checks, race checks, randomized append/rewind/replay transitions, real isolated Codex and TClaude rewinds, and active-branch native Fork idempotency. The release acceptance record separately covers automatic Hooks and Hosted synchronization; model summaries are not a guarantee of code correctness.

# Coconet 0.22.1

Connecting a folder and the installation guide now explain what is shared with project members: compact conversations, original Session records, and work progress. Existing history is not imported automatically, but continuing an existing Session may share earlier content. The guide also explains local model usage and the current absence of end-to-end encryption.

Dashboard status distinguishes unsynced projects, unpublished graphs, pending work, and processed content that produced no nodes. A failed refresh keeps the previous graph and clearly marks it as an earlier result. Runtime logs present the current situation and next step before collapsed diagnostic details. Keyboard focus returns correctly when closing the dialog.

Local diagnostics distinguish failed copying from requested replica repair, explain automatic retries versus paused tasks, and provide recovery steps for changed sources, full disks, timeouts and recognized Claude authentication or rate-limit errors. Model prompts, model selection, Session retention and synchronization scheduling are unchanged. Existing accounts, projects and directory connections are preserved.

连接页和安装引导现在明确会向项目成员共享精简对话、原始会话记录及工作进展。已有历史不会自动导入，但继续旧会话时可能共享此前内容；同时说明本地模型用量和尚未端到端加密的边界。

工作图区分未同步、尚未发布、待处理和已处理但无节点。刷新失败保留旧图，并明确标为上次结果。运行日志先说明当前情况和下一步，技术详情默认折叠；关闭弹窗后的键盘焦点恢复已修复。本地诊断区分复制失败、等待补传、自动重试和暂停，并提供对应恢复指引。

本次不改变构图 Prompt、模型选择、会话保留与同步调度；已有账号、项目和目录连接保持不变。

Validation: full repository checks, 38 Dashboard tests, production-handler browser tests in Chinese and English at 1440 / 390 / 320 px, keyboard navigation, failed-refresh recovery, and local CLI / MCP error handling. Synthetic fixtures were used; no user history was modified and no new model-quality claim is made.

Client / Server / npm / both Plugins use 0.22.1. macOS and Linux bundles include archive SHA-256 checksums and release provenance. The public repository contains distribution documents and artifacts only.

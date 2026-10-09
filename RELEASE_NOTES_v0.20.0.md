# Coconet 0.20.0 — Local DAG / Relay MVP

Client / Server / npm / Codex Plugin / Claude Plugin: **0.20.0**.

DAG generation moves to user devices. A locally configured Codex app-server or Claude print executor first maintains work stages and then determines dependencies. The server now handles account permissions, ordered synchronization, retained content and published graph checkpoints without model calls.

DAG 生成迁至用户设备，由独立配置的 Codex app-server 或 Claude print 先维护工作阶段，再判断依赖。服务器负责账号权限、提交同步、内容持久化与图检查点，不再调用模型。

- Explicit inference login, model selection, rolling call budget, pause/resume and persistent diagnostics.
- Durable local capture, traceable decisions and deterministic graph replay; automatic SessionEnd seals the current stage without a model call when visible text is unchanged.
- Cross-device graph synchronization, fixed-version compact reads, explicit native Fork and Library operations.
- A Dashboard reading published checkpoints, including offline producers; bilingual installation guidance and fixed-source CLI actions.
- Reference-aware retention, local archive limits, Relay quotas, complete backup and recovery. New Relay objects use local disk.
- Retired server inference, old semantic APIs and old Dashboard body/mutation paths removed from the active workflow.

本地推理支持显式登录、模型与预算、暂停恢复和持久诊断；自动会话结束能在没有新正文时确定性封存阶段。跨设备图同步、固定版本读取、同类型原生接力、Library 与离线网页查看均接入新流程。保留与回收按有效引用执行，完整备份覆盖身份、账号库、旧内容和新 Relay。

## Upgrade

```sh
npm install --global coconet@0.20.0
coconet version
coconet connect
coconet inference login --provider codex
coconet inference configure --provider codex --model YOUR_MODEL --max-calls 20
```

Use `--provider claude` for Claude inference. Reconnect each previously bound directory, then start a new Agent session. Existing account/device/project identities are preserved. Native history is unchanged and is not imported automatically. The old server DAG is not migrated; old clients must upgrade. A project stays empty in the new Dashboard until a client publishes its new graph.

每个旧关联目录需重新 `connect` 切换工作流，然后配置本地推理并重启 Agent。账号、设备、项目身份保留，原生历史不变且不自动导入；旧 DAG 不自动迁移，旧客户端必须升级。新网页在首次发布图之前显示为空。

## Validation and limits

Engineering checks include the complete repository check, Runtime/Server race tests, production-handler browser tests and isolated macOS/AnyDev installed-client collaboration. Real conversations covered discovering teammates' work, adopting it, small revisions, both native Fork/Resume paths, offline reads and graph/replay agreement. A 5,000-node chain was rendered in a local synthetic capacity check; this is not a server throughput or arbitrary large-graph guarantee. Same-input comparisons found comparable stage boundaries after separately diagnosing a legacy equality defect; no broad model-quality improvement is claimed.

工程、浏览器与本机/开发机真实协作验收通过；模型输出仍存在场景边界，超长会话分段和复杂大分支图优化后置。容量样本不能换算为用户并发承诺。

**No end-to-end encryption in this MVP:** administrators can read uploaded content. Full processing archives primarily remain on the executing device. Encryption, automatic old-data migration and the proposed team task queue are not part of this release.

**本 MVP 尚未端到端加密**，管理员可读取上传内容；完整处理档案主要留在执行设备。加密、旧数据自动迁移与团队任务队列均不在本次发布范围内。

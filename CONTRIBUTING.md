# Contributing

Maintenance focuses on security, correctness, recovery and compatibility for the
supported Codex, Claude Code and Cursor CLI integrations. The product scope is
automatic work awareness, source lookup and optional session continuation. New
Agent integrations and task assignment or approval systems need a separate scope
discussion; they are not prerequisites for maintaining the existing product.

Discuss significant behavior or protocol changes in an issue before preparing a
large patch. Small fixes can go directly to a pull request. Describe the problem,
the resulting behavior and how you verified it.

Run `make check` before submitting. Add focused regression tests for state changes,
failure recovery and protocol boundaries. Use generated fixtures; do not commit
conversation histories, tokens, production URLs with access parameters, personal
paths, database snapshots or model output containing private work.

Dashboard changes follow [DESIGN.md](apps/dashboard/DESIGN.md). Keep English and
Chinese strings aligned, reuse components, and check desktop / narrow screens,
keyboard navigation and loading / failure states.

Real-Agent tests are optional and require a separate profile and disposable
workspace. They can invoke a paid provider. Never use another person's existing
history or silently change their Agent configuration. Default CI needs no model
credentials. See [development](docs/development.md).

Keep commits focused. Update the relevant public documentation when behavior
changes. Contributions are submitted under the repository's MIT license; retain
third-party attribution when reusing code.

较大的协议或行为改动请先用 Issue 讨论。提交前运行 `make check`；测试使用合成数据，
不提交真实对话或凭据。前端保持双语和统一设计，真实 Agent 验收使用独立配置与工作区。
维护重点是现有 Codex、Claude Code、Cursor CLI 接入的安全、正确性、恢复与兼容。
新增 Agent、任务分派和审批系统需另行讨论，不作为现有产品维护的前置条件。

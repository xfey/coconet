# Coconet 0.27.2 — focused collaboration context / 精简协作上下文

Normal collaboration queries return concise evidence guidance. Health reports describe the connected project, without forwarding other projects' errors, raw stacks or native paths. Full records remain available in `coconet projection status --json` and `coconet diagnostics list` when diagnosis is needed.

正常协作查询使用简短证据提示。健康检查按当前项目汇总，不透传其他项目错误、原生路径或堆栈；需要排查时，仍可查看完整本地诊断。

- Agent instructions keep successful work moving. Unrelated background capture failures do not establish that the current conversation or a teammate's work failed. Query coverage and exact-source read results still limit what can be claimed.
- Operation errors retain actionable recovery steps; long provider logs remain in diagnostics. Claude source lookup distinguishes missing, duplicate and unreadable files.
- New Claude lifecycle tracking skips confirmed empty launches that end without interaction. Older or uncertain missing-file events remain diagnosable and are not automatically discarded. The new input Hook is silent and does not invoke inference.
- Dashboard directory connection offers existing-project selection, shared-link joining and creation. The compact “+” dialog uses the same navigation; input remains available when switching choices.

Agent 指令优先推进用户任务，只有当前操作受阻或用户要求时才排查。Claude 原始文件缺失、重复和不可读分别给出明确提示；有完整证据的无输入空启动不再反复采集，旧记录和证据不足的记录继续保留。目录连接提供已有项目、分享链接和新建三个入口，主页“+”使用一致的紧凑布局。

## Update / 更新

```sh
npm install -g coconet@0.27.2
coconet version
```

Restart the Agent to load the updated Plugin and tool descriptions. Client and Server artifacts are version 0.27.2. Existing projects, connections and native history are preserved; no data migration is required.

升级后重新打开 Agent。已有项目、连接和原始会话保留，无需迁移数据。

## Validation / 验证

Isolated tests cover the real Claude Node helper, lifecycle boundaries, previously captured and pending inherited history, and local daemon/MCP reads with paused background captures in two projects. Graph, search and fixed-source reads remain usable; actual source failures retain recovery guidance. These checks do not call a model or modify original conversations. DAG inference templates are unchanged.

隔离测试覆盖真实 Node helper、空会话生命周期、已采集 / 待发布历史，以及存在后台暂停故障时的图、搜索和固定来源读取。测试不调用模型，不改动原始对话；DAG 推理模板保持不变。

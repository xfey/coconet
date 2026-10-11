# Coconet 0.21.1

Client, Server, npm and both Agent Plugins move to 0.21.1.

- Local dependency inference identifies the specific work result used before matching candidate nodes. Candidate-to-candidate graph links no longer steer that attribution.
- A bounded window of earlier dialogue helps resolve references across stage boundaries. New dependencies still require current, non-inherited conversation evidence.
- Ordinary use of existing code, documentation and framework behavior does not require a collaboration source node. Explicitly adopted work whose source is missing remains pending.
- Node retirement, local Session replicas, search, Fork and Relay storage protocols retain their existing behavior. This update does not automatically rewrite previously accepted graph edges.

本次更新改善 DAG 依赖归属：先识别当前工作真正采用的成果，再匹配来源节点；补充有限前文理解指代，避免相似技术做法或已有连线误导判断。普通仓库代码与文档属于背景信息；明确采用同事成果却缺少来源时，仍会保留待补充状态。

升级保留现有项目与身份，无需重新连接。已有错误连线不会自动重写。MVP 仍未端到端加密。

```sh
npm install --global coconet@0.21.1
coconet version
```

Open a new Agent session after upgrading to load the updated integration. 升级后开启新的 Agent 会话加载更新。

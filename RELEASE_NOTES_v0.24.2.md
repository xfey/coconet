# Coconet 0.24.2

Stage summaries now focus on cumulative outcomes, current state and important constraints. Relationship inference attributes dependencies to the specific work actually adopted, rather than to unrelated shared baselines or later stages that merely retain an earlier capability. Genuine multiple-source dependencies remain supported.

阶段摘要聚焦累计成果、当前状态和关键限制；关系分析按实际采用的具体成果确定来源，减少将共有基础或仅保留既有能力的后续阶段误作依赖。真实的多来源关系继续保留。

Session open items are presented as historical records. The Dashboard offers related follow-up nodes, and Agent guidance checks relevant later work before describing team work as unfinished. Historical items are preserved without automatically marking them complete.

会话中记录的未完成事项与团队当前进展区分呈现；网页提供相关后续节点入口，Agent 在判断团队工作未完成前会检查后续成果。历史记录保留，不自动改成已完成。

Validation used 74 archived requests from a 36-turn collaboration, 26 independent two-model controls, repeated relationship checks and 8 additional real interactive turns on macOS TClaude and Linux Codex. Comparable summaries were about 27% shorter on average; both devices converged with complete retained-source reads. Model semantics remain variable: a weak attribution and occasional compressed wording remain documented. No fixed topology or universal summary-length guarantee is claimed.

验证包括原 36 轮协作的 74 份固定请求、26 份双模型独立对照、重复关系检查，以及本机 TClaude / Linux Codex 追加的 8 轮真实交互。可比摘要平均缩短约 27%，双端图与保留来源读取一致。少量弱归因和表述压缩波动仍存在，不保证唯一图结构或严格字数上限。

Upgrade collaborating clients to 0.24.2 and start a new Agent session to load the updated Skill. Existing projects, identities, folder connections and native histories are preserved. Previously accepted graph decisions are not automatically regenerated. Server 0.24.2 provides the Dashboard update. This release does not change the 0.24.1 wire formats or the production inference timeout.

协作客户端升级到 0.24.2 后，开启新的 Agent 会话以加载更新的 Skill；项目、身份、目录关联和原始历史保留，已有图不会自动重生成。Server 0.24.2 包含网页更新；本次不改变 0.24.1 的协议格式或生产推理时限。

This public repository contains distribution documents and release artifacts only.

# Coconet 0.24.1

Rewinding a conversation now restores the surviving tail to an editable state when it has no remaining dependency references or Library collection and its Session is still open. Subsequent work in the same stage can update the existing node. A colleague’s tail released by the withdrawn work follows the same rule. Protected nodes, earlier stages and ended Sessions remain fixed.

会话回退后，如果保留下来的末尾没有剩余依赖引用或收藏保护，且会话尚未结束，会恢复为可编辑状态；后续同一阶段可继续更新原节点。同事的末尾若因撤回失去最后一个引用，也遵循相同规则。受保护节点、更早阶段及已结束会话保持固定。

Restoration is deterministic and adds no model call. It preserves node text and fixed source, advances revision guards to reject stale results, and leaves accepted historical replay unchanged. Upgrading does not retroactively unlock old nodes.

恢复由确定性规则完成，不增加模型调用，不改写节点文本或固定来源；版本校验会拒绝迟到结果。已接受历史的重放结果保持不变，升级不会批量解锁旧节点。

Upgrade the Server first, then every collaborating client to 0.24.1. New commits use `coconet-json-v5` and checkpoints use `coconet-state-json-v3`; older clients cannot process them. Accounts, projects, identities and folder connections are preserved.

先升级服务器，再将全部协作客户端升级到 0.24.1。新提交与检查点使用新版协议，旧客户端无法处理；账号、项目、身份及目录关联保留。

Validation includes the full engineering suite, race checks, randomized histories, strict Relay and checkpoint replication tests, and isolated replay of real project copies. This is a state-management fix; model semantics and native history parsing are unchanged. This public repository contains distribution documents and release artifacts only.

# Coconet 0.21.0

Client, Server, npm and both Agent Plugins move to 0.21.0.

- Connected devices retain current and referenced project Session content and index compact user/assistant text locally. Fixed-source reads and native Fork use verified local copies when available.
- Hosted content transfers directly through R2 temporary storage, with a default 7-day window and authorized replica repair. Metadata, permissions and published DAG checkpoints remain on the Relay.
- Idle workers share synchronization hints. Dashboard refreshes follow published checkpoints. Successful polling and transfers no longer produce routine HTTP logs; operator bundles include bounded, dedicated journal configuration.
- Device-local health and diagnostics replace the unused remote replica-report interface. Server commit-history compaction remains deferred.

托管开发项目已清空，账号与设备身份映射保留。请更新客户端，在工作目录重新执行 `coconet connect`，创建新项目并开启新的 Agent 会话。原生对话历史不会被清理。

设备后台保留项目所需会话副本，检索在本地完成；对象存储只提供临时转发，默认保留 7 天。缺失内容依赖仍持有副本的授权设备补传，全部副本丢失时不能保证恢复。可使用 `coconet storage export --file /absolute/project.tar` 备份本地项目。

MVP 尚未端到端加密。正式用户无需单独配置推理账号、模型或调用上限，后台沿用来源 Agent 的登录和默认模型。

```sh
npm install --global coconet@0.21.0
coconet connect
```

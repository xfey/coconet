# Coconet 0.22.0

New metadata growth is checked against a shared budget and minimum free disk space. Eligible commit history is compacted only when covered by a durable checkpoint; original device-owned Session content is preserved. Updated clients can catch up after extended offline periods while retaining unpublished work and resolving lost receipts without republishing accepted changes.

Relay maintenance processes bounded batches, retries individual object failures, and exposes backlog and capacity status to a private operator socket. Linux operator bundles now include daily metadata backup, independent restore verification, bounded retention and storage monitoring templates. Online backups support bbolt with external Relay content and no legacy artifact inventory; filesystem content continues to require a stopped-service backup. These local backups do not include temporary object-store Session bodies and do not protect against loss of the whole host.

Upgrade collaborating clients to 0.22.0 before enabling history compaction. Older clients cannot recover a compacted history cursor. Existing accounts, identities, projects and directory connections are preserved; reconnecting is not required.

本次加入共享元数据预算和低磁盘写入保护，只回收已被持久检查点覆盖且经过宽限期的历史。新版客户端可以在长期离线后追平，保留未提交工作，并确认已接受但丢失回执的提交，避免重复发布。原始会话仍由设备持有。

Relay 维护按有界批次执行，对单个对象失败重试，并提供回收积压和容量指标。Linux 制品新增每日备份、独立恢复校验、有限保留和巡检模板；在线方式支持 bbolt + 外部 Relay 内容且无旧 artifact 库存，文件正文后端仍使用停服完整备份。同机备份不包含临时对象存储中的会话正文，也不能防止整机磁盘丢失。

启用历史回收前，请将协作设备全部升级到 0.22.0。旧客户端无法恢复已回收历史的游标。已有账号、身份、项目与目录连接保留，无需重新连接。

Validation covers full repository checks, concurrent-write metadata snapshots and independent restore verification, checkpoint recovery with pending work and lost receipts, capacity refusal, maintenance failure isolation, and Linux testing capped at 4 CPU / 8 GiB. Test fixtures use synthetic protocol data; this release does not change model prompts or claim new model-quality validation.

Client / Server / npm / both Plugins use 0.22.0. macOS and Linux bundles include per-file manifests, archive SHA-256 checksums and release provenance. The public repository contains distribution documentation and artifacts only.

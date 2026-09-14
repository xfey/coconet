# Coconet 0.13.0

Coconet now explains automatic installation and upgrades, with animated download feedback in interactive terminals. `coconet init` immediately shows project status and a complete connection command to share with teammates.

Connection Codes issued by the new Server expire after one hour and can be used repeatedly within that time. The CLI and issuance API no longer expose a remaining-use counter. Already issued codes retain their original expiry; existing project membership continues after a code expires.

## Upgrade

```bash
npm install --global coconet
coconet version
```

Self-hosted operators should update both Client and Server. The Server migrates metadata schema 18 → 19, removes use counters, and keeps previously exhausted codes invalid. Back up metadata and Deployment identity before upgrading; rollback to the old Server requires restoring the pre-upgrade metadata snapshot. The issuance response uses schema 2; requests and routes remain unchanged.

## Validation

- Full engineering checks, concurrent joining, exact expiry boundaries, and race checks passed.
- bbolt and isolated PostgreSQL migration/restart checks preserved existing project and Session data.
- macOS arm64 and Linux amd64 installation checks use isolated mock Agent hosts; no real Agent/model interaction is claimed. Other platforms receive build and applicable signing/notarization verification.

## 中文说明

自动安装 / 升级时会先说明原因，并显示动态下载进度；`coconet init` 完成后直接显示项目状态和可复制的队友加入命令。

新签发连接码有效期为一小时，有效期内不限使用次数。CLI 与接口移除剩余次数；已有连接码保持原到期时间，过期不影响已加入的成员。自部署需配套更新客户端和 Server，数据库会迁移到 schema 19；回退旧 Server 时必须恢复升级前的 metadata 备份。

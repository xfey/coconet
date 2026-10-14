# Coconet 0.25.0

Project IDs now provide a single way to share and join work. This release updates the Client, Server, npm launcher and both Plugins to 0.25.0.

## Changes

- Enter a Project ID directly on the connection page, choose an existing project, or create one. Same-deployment project links are accepted too.
- Use `coconet connect PROJECT_ID` or `coconet connect --project PROJECT_ID`; add `--server` for a self-hosted deployment. Repeated joins are safe, and leaving does not prevent a later explicit rejoin.
- Share one Project ID or project link. A signed-in user with that ID can explicitly join; opening a link or reading a project does not automatically grant membership. Separate invitation codes and their endpoints have been removed.
- Open the Dashboard at `/`; project links use `/#project=…`, and static assets use `/assets/`. `/dashboard/` is no longer a supported entry point.
- Connect displays device and folder information without a visible ten-character authorization code. A random authorization link, explicit approval, PKCE, expiry, account/folder checks and one-time redemption still protect the flow.
- Fix initial folder registration when creating a project with an existing login using `--new`.

## Upgrade

Upgrade the Server and all collaborating clients together. Old clients do not support the new connection capabilities. Hosted development projects have been cleared for a fresh start; accounts, identity mappings and native conversation histories are retained. Reconnect work directories to new projects. Old invitation and project data are not migrated.

```sh
npm install --global coconet@0.25.0
coconet connect
# Or use a teammate's Project ID:
coconet connect prj_…
```

Project IDs are joining credentials: share them with intended collaborators. This update does not change DAG inference or add end-to-end encryption.

## Validation

The full engineering checks, authorization race tests and real CLI/browser flow tests passed. Browser coverage includes three accounts, explicit joining, invalid inputs, failed-approval retry, leaving/rejoining, directory changes, English/Chinese and mobile widths. macOS and Linux clients registered distinct folders against one isolated Linux server. Six platform bundles passed reproducible builds; both macOS architectures passed Apple notarization and explicit ticket verification, with quarantined host binaries starting normally.

## 中文说明

本次统一使用项目 ID 分享和加入：连接页面直接输入 ID，也可选择已有项目或新建项目；终端支持 `coconet connect PROJECT_ID`。项目链接采用 `/#project=…`，看板直接从网站根路径打开。旧邀请码与可见的十位设备授权码已删除，网页明确确认、随机授权链接、PKCE、过期和一次性领取等校验继续保留。

已登录用户持有项目 ID 即可明确加入；只打开链接不会自动加入。请将 ID 分享给希望加入的协作者。客户端与服务器须一起升级。Hosted 测试项目已清空，账号映射、部署身份和原生对话历史保留，请重新连接工作目录并创建或加入新项目。本次不改变 DAG 推理逻辑。

完整工程检查、浏览器与真实 CLI 验收、macOS/Linux 双机连接通过；覆盖双语、窄屏、错误重试、多账号和退出后重新加入。六平台可复现构建、macOS 双架构公证及本机保留隔离标记启动检查通过。

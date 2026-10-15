# Coconet 0.26.0

Client, npm and Plugins: **0.26.0**. Server/Dashboard: **0.26.1**.

## Changes

- Project IDs use `coco_` with the same 128-bit Base58 random payload.
- Project URLs use `/coco_…`; direct opening, reload, login return, sharing and pasted-link joining use the same route. Node selections remain in the fragment. Browser back / forward restores project and node selection.
- Browser tabs use the original transparent coconut icon, without the black app tile.
- The installation entry is “Install Coconet”; the guide title is “Get started with Coconet”, with matching Chinese text.
- Client status links and signed connection capability are updated together. Server and clients require a coordinated upgrade (`project_connections_v3`).

## Upgrade

Run `npm install --global coconet@0.26.0`, then `coconet connect` in your working directory. Hosted development projects were cleared with authorization; account identities, credentials and native histories were retained. Old project IDs are not aliases for new projects. Use `coconet disconnect` before reconnecting if an old folder association remains.

Self-hosted operators: this version changes the accepted project namespace and does not migrate old projects. Back up before resetting development project data; ordinary upgrades never silently delete it.

## Validation

Repository checks, production-handler browser tests, real CLI connection and multi-account joining passed. Coverage includes direct links, OAuth, refresh, project/node history navigation, invalid IDs, offline removal of an old binding, bilingual desktop/mobile layout and response races. DAG inference and native session parsing are unchanged; this release does not claim new model-quality validation.

macOS Runtime / Bootstrap binaries are Developer ID signed and submitted for Apple notarization before publication. Published archives retain fixed SHA-256 verification.

## 中文

- 项目 ID 统一为 `coco_`，随机部分仍为 128-bit Base58。
- 链接直接使用 `/coco_…`，去掉 `#project=`；节点定位信息保留在 fragment。直接打开、刷新、登录返回、分享、粘贴加入及浏览器前进后退均已检查。
- 标签页使用原版透明底椰子图标，不带黑色底板。
- 入口改为“安装 Coconet”，弹窗标题为“开始使用 Coconet”，保持中英文一致。
- 服务端与客户端配套升级。旧托管测试项目已按授权清空，账号、凭据与原生对话历史保留；新测试需重新创建 / 连接项目。旧目录可先运行 `coconet disconnect`。
- 未改变 DAG 推理与会话解析。自部署请先备份；本版本不迁移旧项目，也不会在普通启动时自动清空数据。

# Coconet 0.26.5

Client, npm, both Agent Plugins and Server/Dashboard: **0.26.5**.

## Changes

- MCP tools describe parameter bounds, defaults, pagination, exact source selection and safe retry rules. Validation uses the advertised input schema. `search_work` with `limit: 100` now identifies `limit` as invalid and explains the 1–20 range; use `next_cursor` for additional pages.
- Known tool errors return readable MCP tool results with field-specific issues and recovery guidance. An unconnected work folder is distinguished from invalid arguments. Invalid cursors explain how to restart; Runtime server failures no longer appear as authentication errors.
- Client and server command help share a catalog with the generated parameter manuals. Help supports nested topics and works before login or project connection, without starting inference or writing runtime state. The npm launcher still installs a missing Runtime on first use.
- The signed-out homepage includes decorative ASCII ripples. Drawing pauses in the background, on narrow screens and after login; reduced-motion preferences use a static view. Animation failures do not prevent login. Hosted Debug login is disabled.
- Coconet MIT licenses are included in npm, Plugin, client and server packages. Third-party components retain their own terms. Source publication is being prepared separately; this repository still distributes documentation and artifacts only.

## Upgrade

```sh
npm install -g coconet@0.26.5
coconet version
```

Start a new Agent session to load the updated tool schemas. Existing 0.26.x project connections, data and native histories remain in place. Self-hosted operators can install the matching Linux Server bundle. This update does not change DAG inference rules or automatically import history.

## Validation

Full repository checks, exported-source checks, MCP boundary and recovery cases, installer tests and production-handler browser checks passed. Homepage checks cover bilingual layouts, reduced motion, lifecycle pauses and animation fallback. Release archives are built twice for reproducibility; macOS clients are Developer ID signed and notarized. Published assets and isolated macOS/Linux installations are verified against the frozen candidate.

## 中文

- MCP 参数直接说明范围、默认值、分页和重试规则，实际校验复用同一份 schema。搜索的 `limit` 为 1–20，默认 10；更多结果通过 `next_cursor` 读取。
- 错误明确指出问题字段和下一步操作。未连接目录、分页失效与运行时异常不会再混成含糊的“参数错误”或“认证失败”。
- 补齐客户端、服务端 help 及参数手册，支持子命令帮助；查看帮助无需登录或连接项目。
- 首页增加字符涟漪动画，支持后台暂停、窄屏停止和减少动态效果；动画异常不影响登录。托管 Debug 登录已关闭。
- MIT 许可随包分发，第三方条款保留。开源源码另行准备，本公共仓库仍仅分发文档和制品。
- 升级后重新打开 Agent 加载新工具说明。现有 0.26.x 连接、项目与原始对话保留；DAG 推理规则保持不变。

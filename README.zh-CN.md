# <img src="apps/dashboard/assets/coconet-app-icon.svg" alt="" width="40" height="40" align="absmiddle"> Coconet

[coconet.space](https://coconet.space/)

[English](README.md) · 简体中文

和同事共享 session，共同推动工作进展。了解同事的工作，并从已有的进度继续。

Coconet 将团队的 Agent session 整理成工作图：可以看到项目进展、查找工作内容，并且可以直接 fork 同事的对话并继续工作。

支持 **Codex、Claude Code 和 Cursor CLI**，可在 macOS、Linux 上运行。

## 开始使用

一行命令即可完成安装：

```sh
npm install -g coconet
```

安装后，在你希望参与协作的工作目录中执行：

```sh
coconet connect
```

在弹出的网页中登录（Linux 需要手动点击网页链接），选择加入/创建项目，即可构建一个在线共享工作区。

把项目 ID 或项目链接发给同事，对方可以在自己的工作目录连接到同一个项目：

```sh
coconet connect coco_PROJECT_ID
```

请将 `coco_PROJECT_ID` 替换为完整的项目 ID。默认网页看板：<https://coconet.space/>。

连接后会同步新的对话更新，已有历史不会自动全部导入。如果只希望查看网页上的进展
不参与协作，则直接打开链接即可，无需安装。

## 可以怎样协作

正常使用 Agent 工作即可自动进行进展同步。需要协作时，可以直接问：

- “看看其他同事在做什么。”
- “之前登录这块是怎么决定的？是哪位同事设计的来着？”
- “请你找到同事的会话并且拉下来，我们继续完成剩余工作。”

Agent 可以通过 Coconet 工具了解工作图、搜索和读取会话内容、完成会话接力。不过，接力不会自动还原对应时间的代码，需要自行确认当前代码状态，建议通过 `git` 保持代码同步。

## 数据和推理在哪里

工作图由成员设备上的 Agent 生成，沿用该 Agent 的登录、提供方和默认模型，无需另配模型密钥或调用次数。后台推理会使用执行设备对应 Agent 的额度。

服务器负责账号和项目管理、同步协调、工作图检查点以及临时内容中转。客户端在后台保存项目保留会话的本地副本，正文检索在本地进行。

项目 ID 可供已登录用户加入项目，请只分享给协作者，务必不要泄漏分享链接/项目ID给不认识的人。

更多说明见[数据共享与保留](docs/data-sharing.md)和[架构与数据流](docs/architecture.md)。

## 命令帮助

```sh
coconet --help
coconet help connect
coconet history preview --help
coconet-server help init
```

[命令参数手册](docs/cli.md)集中列出了客户端和服务端命令的参数、默认值与示例。[MCP 工具说明](docs/mcp-tools.md)介绍工具参数、分页、会话来源和错误处理。

## 使用自己的服务器

Coconet 支持在 Linux 上部署独立服务器，以适应私有化部署、局域网协作等场景。

需要连接到自部署服务器时，在连接参数中，额外指定服务器地址：

```sh
coconet connect --server 'https://coconet.example.com'
coconet connect coco_PROJECT_ID --server 'https://coconet.example.com'
```

部署步骤、身份认证、HTTPS 和存储配置见[自部署指南](docs/self-hosting.md)。

## 从源码构建

需要 Go 1.26.9+、Node.js 24.22+、npm、Python 3 和 Git。

```sh
go build -o bin/coconet-server ./apps/server
make check
```

客户端构建、浏览器测试和打包步骤见[开发文档](docs/development.md)。

## 参与项目

- [贡献指南](CONTRIBUTING.md)
- [安全问题反馈](SECURITY.md)
- [文档索引](docs/README.md)

## 许可证

Coconet 自有源码使用 [MIT 许可证](LICENSE)。第三方组件保留各自许可，详见[第三方说明](THIRD_PARTY_NOTICES.md)；可选的 Claude Agent SDK 不属于 MIT 授权范围。

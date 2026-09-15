# Coconet

Coconet is team infrastructure for sharing, searching, reading, and handing off Coding Agent Sessions across Codex and Claude Code.

Coconet 是面向小团队 Coding Agent 协作的 Session 共享、检索、阅读与同类型接力基础设施。

## Distribution status

This repository is the public distribution endpoint for Coconet releases. Product implementation source is not published here yet. GitHub's automatically generated “Source code” archives contain only this repository's public release documentation, not the source used to build Coconet binaries.

当前仓库作为 Coconet 的公开分发入口，尚未发布产品实现源码。GitHub 自动生成的 “Source code” 压缩包只包含本仓库的公开发布说明，不是 Coconet 二进制的构建源码。

GitHub Client / Server version [0.16.0](https://github.com/xfey/coconet/releases/tag/v0.16.0) adds project folder registration and consistent canvas notifications. Hosted is running 0.16.0; npm `latest` remains 0.15.1 until the account holder completes publication verification. `0.4.0` remains the first release under the new product identity, while historical `v0.1.0`–`v0.3.0` tags, Release assets, and npm versions remain immutable records of the former Coddis identity.

The current release supports:

- GitHub account login on Hosted, with independent device credentials and browser sessions
- explicit device and folder confirmation, desktop folder selection, and a shared existing/new project connection card
- browser-based invitations with ready-to-copy project connection commands, profile editing and device management
- OIDC or administrator-issued Account Keys for private deployments
- concise Agent plugin setup summaries and retry commands that preserve custom instances
- visible installation and upgrade progress, with command output kept separate
- automatic browser opening, a prominent verification code and direct navigation to the project after approval
- concise connection confirmation, with account and upload details available through `coconet status`
- project sharing, language and current-project settings in the bottom Dashboard toolbar
- a dedicated plus button for creating or joining projects, and an account menu for profile, devices and sign-out
- compact primary and secondary buttons, with grouped zoom controls
- project settings that list members, devices and the working folders they have connected (latest registration, not live presence)
- invitation loading, failure and retry feedback in both sharing entry points
- canvas-relative notifications that avoid the details panel, and automatic centering when new nodes appear
- one-hour Connection Codes with no per-code use quota

- goal-oriented DAG stages that keep related discussion, implementation, tests, documentation, and corrections together
- historical Session overviews that preserve major contributions and avoid attributing verified inherited prefixes twice
- macOS: Apple Silicon and Intel
- Linux: `arm64` and `x86_64`
- automatic default Codex and Claude Code setup, with optional additional Agent instances
- local-first automatic synchronization of project-scoped Agent Sessions
- cumulative Session history that remains searchable after Agent context compaction
- Agent-driven browsing of recently synchronized team Sessions without inventing a search query
- project Work DAGs with current work, sealed checkpoints, outstanding items, and fixed evidence
- a Session Library of user-selected immutable snapshots, with server-generated topic tags and browsing
- Agent-driven work discovery, fixed evidence reads, and same-Agent handoff with source lineage
- fixed Activity history for checkpoints, Library collections, and registered Forks
- a bilingual, draggable Work DAG canvas with ELK Layered routing and project Session Library shortcuts
- on-demand failure explanations in the CLI and diagnostic API, with bounded history and log correlation
- a project Dashboard with fixed-source conversation viewing and Library collection controls at `https://api.coconet.space/dashboard/`
- Agent-driven lexical query and collaboration-root-relative path search
- fixed-version Session reading, local pulling, and same-Agent forking
- Git repositories and explicitly selected non-Git collaboration workspaces
- stable Project UIDs and shorter, short-lived approval-free Connection Codes
- local folder disconnection without changing project membership or other devices
- Hosted use and self-hosted Server deployment
- canonical Hosted API at `https://api.coconet.space`
- self-hosted filesystem storage by default, with optional S3-compatible storage
- explicit user-level uninstallation of Agent Plugins, Marketplace entries, Hook trust, caches, and the native Runtime, with optional local-data purge

Windows is not currently supported.

## Installation

The recommended installation is:

```bash
npm install --global coconet
coconet version
```

The npm package installs a stable lightweight launcher in npm's existing global bin directory. On first use, or when the npm package version changes, the launcher downloads only the archive for the current OS and CPU from this repository's matching immutable Release, verifies its pinned SHA-256 and bundle manifest, and installs the native Runtime without `sudo`. It continues the original command in the same terminal; no additional `PATH` export or new terminal is required when npm's own global bin directory is already available.

The launcher explains why setup is needed and shows a spinner with download progress in an interactive terminal. Redirected output uses plain stage lines. Setup feedback goes to stderr, preserving JSON and other command protocols on stdout. Matching installations start directly without setup feedback.

Runtime installation automatically registers the default `codex` and `claude` commands and configures their Coconet Plugins. Configuration roots follow `CODEX_HOME` / `CLAUDE_CONFIG_DIR`, falling back to `~/.codex` / `~/.claude`. Missing Agents are skipped, and one failed setup does not undo Runtime installation or block the other Agent. Start a new Agent session and accept its Hook trust prompt if shown. Installation alone does not connect a project or upload a conversation.

Run `coconet agent list --verbose` to inspect setup and copy the retry command for the intended instance. Defaults can also be configured with `coconet agent add codex` / `coconet agent add claude`. Defaults are added once; removing an integration is respected on later upgrades. To install only the Runtime, set `COCONET_SKIP_AGENT_SETUP=1` for its first launch. Additional instances can be registered explicitly:

```bash
coconet agent add codex --command tcodex --config-root ~/.tcodex
coconet agent add claude --command tclaude --config-root ~/.tclaude
coconet agent list
```

Use `coconet` directly for normal commands. The setup layer is an internal launcher implementation, and `npx` is not part of the supported installation path.

The Linux Server is distributed as a separate operator archive in the same GitHub Release and is not installed by the user-level npm command.

After upgrading, run `coconet status` in an existing connection to register its working folder in Project Settings. This shares its path with authorized project members; directory files are not uploaded.

## Connect a project

Run this in the project directory:

```bash
coconet connect
```

Your browser opens to sign in and review the CLI device and effective folder. Choose an existing project or create one, then approve sharing the folder's existing and future Sessions. Use `coconet connect --path DIRECTORY` to select another folder. On macOS and supported Linux desktops, the page can ask the CLI to open the system folder chooser. Git subdirectories use the repository root, shown explicitly before approval. After approval, the browser shows the selected project and the CLI confirms the connection. Match the verification code shown in the terminal and browser; no typing is needed. If the browser cannot open automatically, use the printed link. Run `coconet status` for account, upload status and the Dashboard link, or use `connect --verbose` for details.

Use Share project in the bottom Dashboard toolbar to invite teammates. Settings contains the current project's folder connection, invitation and leave actions. The plus button beside Projects only creates or joins projects. Click the user icon and name to manage your profile or sign out. They open the invitation link, sign in and accept, then copy the provided `coconet connect --project ID` command and run it in their own working folder. Invitations expire in one hour and have no use counter. Joining in the browser does not upload local content; connecting a folder is a separate step.

For SSH or remote terminals, use `coconet connect --no-browser` and open the printed link on your own computer. The folder always belongs to the machine running the CLI; use `--path` on the server to change a remote folder. `init` remains an alias of `connect`. `connect --project ID` preselects a project in the browser; `connect --new` and `status --invite` / `connect <code>` retain direct workflows. `coconet login` renews device access; `logout` signs out only this device, and `disconnect` unlinks only the current folder. Leave a project or revoke a device from Dashboard settings.

Local upload status describes this device's queue for that server; an empty queue does not prove that Hooks or graph processing have completed.

Human output uses light styling in interactive terminals and plain text when redirected or when `NO_COLOR` / `TERM=dumb` is set. Run `coconet --help` for a task-oriented command overview.

## Shared work and Session Library

Automatic synchronization keeps project Sessions available. Adding a Session to the Library is an explicit selection of a fixed version for later reuse; later conversation changes do not rewrite that entry. Every collected entry has a sealed Work DAG checkpoint. The original Session artifact remains available for same-Agent resume and fork; topic labels use visible user and assistant text, while work summaries use bounded evidence that can include tool facts.

Ask your Agent to find related project work, read a selected source, or add the current Session to the Library. You can also collect an existing version directly:

```bash
coconet library add --session SESSION_ID --version VERSION
coconet library list
```

Sign in to the Hosted Dashboard with GitHub. A resumed Session carries the source conversation; it does not restore source code, dependencies, or the previous working tree. In the Dashboard, select a node and use the Library switch to add or remove its fixed source. Removing the Library entry preserves the original Session, version and DAG node. Interface language is available under Settings in the bottom toolbar; project and conversation text stays unchanged. The Library menu is project-scoped; Chinese “星标会话” maps to English “Session Library”.

Self-hosted operators enable semantic projection with an OpenAI-compatible inference endpoint. Without inference configuration, the existing Session synchronization, Search, Read, Pull, and Fork remain available. For Server upgrades, back up metadata and deployment identity, and restore the matching database when rolling back across schema versions. Server 0.14.0 migrates metadata schema 19 → 20. Upgrade the Server before new clients. Existing projects and Sessions are preserved; signing in through GitHub creates a separate account from earlier Git-profile identities. Accounts are not automatically merged or granted old project membership. An existing member can invite the new account. Rolling back to a schema 19 Server requires the matching pre-upgrade metadata snapshot. Upgrade to current Agent host runtimes when transferring native histories between devices.

## Private server login

Set `COCONET_AUTH_PROVIDER=oidc` with `COCONET_AUTH_ISSUER`, `COCONET_AUTH_CLIENT_ID` and `COCONET_AUTH_CLIENT_SECRET`, or use the default `key` provider. OIDC requires HTTPS; its callback is your server origin followed by `/v1/auth/callback`. Provider secrets stay in the server environment. Private deployments keep their own accounts, projects and Dashboard; Hosted does not proxy private data.

In Key mode, an operator issues an account with `coconet-server account create --config PATH --name NAME`; stop a bbolt server first. The user signs in to the private Dashboard with that key and approves the CLI. Run `coconet connect --server https://your-server.example` to connect. GitHub can also be configured with `COCONET_AUTH_PROVIDER=github` and an OAuth App using the same callback path.

## Uninstallation

Remove Coconet-managed Agent integrations and the native Runtime before removing the stable npm launcher:

```bash
coconet uninstall
npm uninstall --global coconet
```

The first command preserves local project bindings and credentials by default. Use `coconet uninstall --purge` to also remove Coconet-owned local state and credentials. It does not remove native Agent Sessions or call the Server to leave projects.

## Integrity and macOS trust

Full Client / Server releases include six platform archives, their per-archive `.sha256` files, a complete `SHA256SUMS`, and `release.json`. The Server-only 0.15.2 release contains two Linux Server archives, their checksums, `SHA256SUMS`, and `server-release.json`; existing Client releases and npm versions remain unchanged. macOS Runtime and Bootstrap binaries are signed with Developer ID, use hardened runtime and a secure timestamp, and are notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

## Source and licensing

No open-source license currently applies to the Coconet binaries or private implementation. If source components are published here later, their applicable license and contribution boundary will be stated explicitly with those files.

当前 Coconet 二进制及未公开实现不适用开源许可证。后续若在本仓库公开部分源码，会随对应文件明确标注许可证与贡献边界。

Third-party components keep their own licenses. The Dashboard includes unchanged ELK 0.12.0 code under EPL-2.0; its source is available from [elkjs](https://github.com/kieler/elkjs/tree/ff5771d7165445c42c408bb8a090c8035272218c) and [Eclipse ELK](https://github.com/eclipse-elk/elk). The Server distributes its full license and attribution at `/dashboard/vendor/elk-LICENSE.md` and `/dashboard/vendor/README.md`.

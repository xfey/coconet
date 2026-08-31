# Coconet

Coconet is team infrastructure for sharing, searching, reading, and handing off Coding Agent Sessions across Codex and Claude Code.

Coconet 是面向小团队 Coding Agent 协作的 Session 共享、检索、阅读与同类型接力基础设施。

## Distribution status

This repository is the public distribution endpoint for Coconet releases. Product implementation source is not published here yet. GitHub's automatically generated “Source code” archives contain only this repository's public release documentation, not the source used to build Coconet binaries.

当前仓库作为 Coconet 的公开分发入口，尚未发布产品实现源码。GitHub 自动生成的 “Source code” 压缩包只包含本仓库的公开发布说明，不是 Coconet 二进制的构建源码。

Coconet `0.5.0` is the current release. `0.4.0` remains the first release under the new product identity, while historical `v0.1.0`–`v0.3.0` tags, Release assets, and npm versions remain immutable records of the former Coddis identity.

The current release supports:

- macOS: Apple Silicon and Intel
- Linux: `arm64` and `x86_64`
- Codex and Claude Code user-level integration
- local-first automatic synchronization of project-scoped Agent Sessions
- cumulative Session history that remains searchable after Agent context compaction
- Agent-driven query and collaboration-root-relative path search
- fixed-version Session reading, local pulling, and same-Agent forking
- Git repositories and explicitly selected non-Git collaboration workspaces
- stable Project UIDs and shorter, short-lived approval-free Connection Codes
- one `disconnect` flow for remote Membership revocation and local binding cleanup
- Hosted use and self-hosted Server deployment
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

Use `coconet` directly for normal commands. `coconet-setup` is an explicit recovery and local-artifact entry point; `npx` is not part of the supported installation path.

The Linux Server is distributed as a separate operator archive in the same GitHub Release and is not installed by the user-level npm command.

## Uninstallation

Remove Coconet-managed Agent integrations and the native Runtime before removing the stable npm launcher:

```bash
coconet uninstall
npm uninstall --global coconet
```

The first command preserves local project bindings and credentials by default. Use `coconet uninstall --purge` to also remove Coconet-owned local state and credentials. It does not remove native Agent Sessions or call the Server to leave projects.

## Integrity and macOS trust

Each Release includes six platform archives, their per-archive `.sha256` files, a complete `SHA256SUMS`, and `release.json`. macOS Runtime and Bootstrap binaries are signed with Developer ID, use hardened runtime and a secure timestamp, and are notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

## Source and licensing

No open-source license currently applies to the Coconet binaries or private implementation. If source components are published here later, their applicable license and contribution boundary will be stated explicitly with those files.

当前 Coconet 二进制及未公开实现不适用开源许可证。后续若在本仓库公开部分源码，会随对应文件明确标注许可证与贡献边界。

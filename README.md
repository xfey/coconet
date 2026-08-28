# Coddis

Coddis is team infrastructure for sharing, searching, reading, and handing off Coding Agent Sessions across Codex and Claude Code.

Coddis 是面向小团队 Coding Agent 协作的 Session 共享、检索、阅读与同类型接力基础设施。

## Distribution status

This repository is currently the public distribution endpoint for Coddis releases. Product source code is not published here yet. GitHub's automatically generated “Source code” archives contain only the public files in this repository and are not the source used to build Coddis binaries.

当前仓库仅作为 Coddis 的公开分发入口，尚未发布产品实现源码。GitHub 自动生成的 “Source code” 压缩包只包含本仓库的公开文件，不是 Coddis 二进制的构建源码。

The current preview Client, npm, Server, and Hosted release is `0.3.0`, supporting:

- macOS: Apple Silicon and Intel
- Linux: `arm64` and `x86_64`
- Codex and Claude Code user-level integration
- Agent-driven discovery of relevant team Session context
- Query and path-based Session search, with automatic recognition of symbols and error tokens
- Hooks dedicated to automatic Session synchronization rather than automatic search
- Hosted use and self-hosted Server Preview
- Stable Project UIDs, short-lived Connection Codes, and approval-free peer membership
- Self-hosted filesystem storage by default, with optional S3-compatible storage

Windows is not currently supported.

## Installation

Recommended installation:

```bash
npm install --global coddis
coddis version
```

The `coddis` npm package installs a stable lightweight launcher in npm's existing global bin directory. On first use, or when the npm package version changes, the launcher downloads only the archive for the current OS and CPU, verifies the exact SHA-256 embedded in that npm version, validates archive paths, modes, and the bundle manifest, and installs the native Runtime without `sudo`. It then continues the original command in the same terminal. When the matching Runtime is already active, the POSIX shell launcher directly `exec`s it; Node is not retained as a parent process for daily commands.

No additional `~/.local/bin` export or new terminal is required, provided npm's own global bin directory is already in the current `PATH`. Use `coddis` directly for normal commands and do not add Coddis as a dependency of an application repository. `coddis-setup` remains available only as an explicit recovery and local-artifact entry point. `npx` is not part of the supported installation path.

To install, upgrade, or roll back to an exact published Client version:

```bash
npm install --global coddis@0.3.0
coddis version
```

An existing `0.2.1` installation may have left a native Runtime symlink in its user-level bin directory. If npm's global prefix uses that exact same bin directory, npm safely stops with `EEXIST`; remove the path only after verifying that it is the Coddis-owned symlink to `current/bin/coddis`, then retry. Do not use `--force` to overwrite an unknown command. If the old Runtime bin precedes npm's bin in `PATH`, run the newly installed `coddis-setup` once to switch `current`; fresh installations do not need this step.

`v0.3.0` makes team-context recall Agent-driven. Agents can decide when shared Session context is relevant, search it using a query plus optional repository-relative paths, inspect fixed Session versions, and pull or fork a Session when needed. Search recognizes symbols and error tokens directly from the query. Hooks remain responsible for Session synchronization and no longer initiate team searches from every user prompt or file operation. The Client, npm package, Hosted Server, and separate Linux Server operator archives are all version `0.3.0`.

`v0.2.2` changes only the npm installation and launcher boundary. It does not change the Project, Session, or Server protocol. The compatible Hosted Server and separate Linux Server operator archives remain version `0.2.0`.

`v0.2.1` is a Client/npm-only correction. It restores the signed bundle's fixed modes under restrictive process umasks and separates the npm setup command from the native Runtime command. It does not change the Project, Session, or Server protocol. The compatible Hosted Server and separate Linux Server operator archives remain version `0.2.0`.

`v0.2.0` replaces the original repository declaration, join request, approval, and owner-management flow with `coddis init`, `coddis status`, and `coddis connect <connection-code>`. Project bindings are local to each machine. Existing `0.1.1` declarations are preserved during verified migration and are never deleted, rewritten, or staged automatically.

`v0.1.0`, `v0.1.1`, `v0.2.0`, and `v0.2.1` remain available as immutable release history. No previous Tag, npm version, or asset was replaced.

The Linux Server is distributed as a separate operator archive in the same GitHub Release and is not installed by the user-level npm command.

## Integrity and macOS trust

Each Release includes per-archive `.sha256` files, a complete `SHA256SUMS`, and `release.json`. macOS Runtime and Bootstrap binaries are signed with Developer ID, use hardened runtime and a secure timestamp, and are notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

## Source and licensing

No open-source license currently applies to the Coddis binaries or private implementation. If source components are published here later, their applicable license and contribution boundary will be stated explicitly with those files.

当前 Coddis 二进制及未公开实现不适用开源许可证。后续若在本仓库公开部分源码，会随对应文件明确标注许可证与贡献边界。

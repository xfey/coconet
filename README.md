# Coddis

Coddis is team infrastructure for sharing, searching, reading, and handing off Coding Agent Sessions across Codex and Claude Code.

Coddis 是面向小团队 Coding Agent 协作的 Session 共享、检索、阅读与同类型接力基础设施。

## Distribution status

This repository is currently the public distribution endpoint for Coddis releases. Product source code is not published here yet. GitHub's automatically generated “Source code” archives contain only the public files in this repository and are not the source used to build Coddis binaries.

当前仓库仅作为 Coddis 的公开分发入口，尚未发布产品实现源码。GitHub 自动生成的 “Source code” 压缩包只包含本仓库的公开文件，不是 Coddis 二进制的构建源码。

The current preview release is `0.2.0`, supporting:

- macOS: Apple Silicon and Intel
- Linux: `arm64` and `x86_64`
- Codex and Claude Code user-level integration
- Hosted use and self-hosted Server Preview
- Stable Project UIDs, short-lived Connection Codes, and approval-free peer membership
- Self-hosted filesystem storage by default, with optional S3-compatible storage

Windows is not currently supported.

## Installation

Recommended installation:

```bash
npx coddis@0.2.0
```

The `coddis` npm package is a thin installer. It downloads only the archive for the current OS and CPU, verifies the exact SHA-256 embedded in that npm version, validates archive paths and the bundle manifest, and installs Coddis in the current user's home environment without `sudo`.

`v0.2.0` replaces the original repository declaration, join request, approval, and owner-management flow with `coddis init`, `coddis status`, and `coddis connect <connection-code>`. Project bindings are local to each machine. Existing `0.1.1` declarations are preserved during verified migration and are never deleted, rewritten, or staged automatically.

`v0.1.0` and `v0.1.1` remain available as immutable release history. No previous Tag, npm version, or asset was replaced.

The Linux Server is distributed as a separate operator archive in the same GitHub Release and is not installed by the user-level npm command.

## Integrity and macOS trust

Each Release includes per-archive `.sha256` files, a complete `SHA256SUMS`, and `release.json`. macOS Runtime and Bootstrap binaries are signed with Developer ID, use hardened runtime and a secure timestamp, and are notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

## Source and licensing

No open-source license currently applies to the Coddis binaries or private implementation. If source components are published here later, their applicable license and contribution boundary will be stated explicitly with those files.

当前 Coddis 二进制及未公开实现不适用开源许可证。后续若在本仓库公开部分源码，会随对应文件明确标注许可证与贡献边界。

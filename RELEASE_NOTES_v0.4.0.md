# Coconet v0.4.0

Coconet `0.4.0` is the first release under the Coconet product, CLI, npm, Plugin, MCP, Server, and distribution identity. Existing `v0.1.0`–`v0.3.0` releases remain unchanged as historical Coddis artifacts.

## Highlights

- Initialize a project with a stable Project UID and share a short-lived Connection Code through `coconet status`; peers join with `coconet connect <code>` without an approval queue.
- Automatically synchronize only the current project's TCodex and TClaude Sessions through lifecycle Hooks.
- Let Agents actively search shared team Sessions using a query and optional repository-relative paths, then inspect one fixed version.
- Pull an exact Session snapshot for deeper local analysis and fork one pulled same-Agent Session when explicitly requested.
- Install both Agent Plugins and the `coconet-team-context` Skill at user scope.
- Run against the Hosted endpoint at `https://api.coddis.work` or a self-hosted `coconet-server` reachable by domain or `IP:port`.
- Use local filesystem artifact storage by default for self-hosted deployments, with optional S3-compatible storage.

## Install

After npm publication is complete:

```bash
npm install --global coconet@0.4.0
coconet version
```

The global npm launcher installs the signed native Runtime for the current platform from this exact GitHub Release and runs `coconet` immediately in the same terminal. `npx` is not part of the supported installation path.

Linux Server operator archives are available separately in this Release.

## Integrity and platform support

Release assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the complete release set. macOS Runtime and Bootstrap are Developer ID signed, hardened, timestamped, and notarized by Apple; bundled Node retains its upstream signature.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

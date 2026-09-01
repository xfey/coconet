# Coconet v0.7.0

Coconet `0.7.0` adds direct recent team Session discovery and restores the complete Search tool contract in Claude Code. Client, Server, npm, and both Agent Plugins advance together to `0.7.0`.

## Highlights

- Use `list_recent_sessions` when asking broadly what teammates recently worked on. It does not require a search query and returns synchronized Session activity ordered by recency.
- Review each recent result by author, Agent type, activity state, bounded excerpt, recent collaboration-root-relative paths, lineage, and a fixed reference for deeper reading.
- Keep `search_sessions` focused on relevant work. Provide a lexical `query`, relative `paths`, or both; at least one is required.
- Restore `search_sessions` in TClaude by removing the top-level conditional JSON Schema that Anthropic rejects while preserving Runtime and Server validation for empty input.
- Use `read_session` with the fixed Session Version returned by Recent or Search so later synchronization cannot silently change the evidence being read.
- Keep discovery Agent-driven. Hooks continue to synchronize Agent lifecycle boundaries and do not automatically search or inject team content into every prompt or file edit.
- Treat recent timestamps and activity states as synchronized Session evidence, not proof that a teammate is currently online or that work is complete.

## Install or upgrade

```bash
npm install --global coconet@0.7.0
coconet version
```

The global npm launcher downloads only the signed native Client archive for the current platform from this exact GitHub Release, verifies its pinned SHA-256 and bundle manifest, and installs or upgrades the Runtime in the same terminal. Registered Agent integrations are resynchronized automatically after the Runtime upgrade.

## Server upgrade note

Recent Session discovery requires the `0.7.0` Server API. Upgrade a self-hosted Server before distributing the `0.7.0` Client. The upgrade is additive and preserves Deployment identity, Projects, Memberships, immutable Session Versions, artifacts, normalized indexes, lineage, and audit records.

The Hosted endpoint at `https://api.coddis.work` runs `coconet-server 0.7.0` for this release.

## Integrity and platform support

Client assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Separate Linux Server operator archives cover both architectures. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the exact set.

macOS Runtime and Bootstrap binaries are Developer ID signed, hardened, timestamped, and notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

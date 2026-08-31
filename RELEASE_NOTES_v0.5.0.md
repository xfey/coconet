# Coconet v0.5.0

Coconet `0.5.0` improves Session continuity, offline capture, workspace flexibility, and the project connection flow. Client, Server, npm, and both Agent Plugins advance together to `0.5.0`.

## Highlights

- Preserve all normalized Turns already observed by Coconet when Codex or Claude Code compacts or rewrites its active context. Search, fixed-version Read, and Pull transcripts use the cumulative history while original Agent artifacts remain immutable.
- Save each prepared observation and artifact snapshot locally before validating the remote Deployment. A temporary network outage no longer delays the first durable capture or causes a later compacted view to replace it.
- Use a Git top-level as the preferred collaboration root, or explicitly use an ordinary non-Git directory. Sync, Search, Read, and Pull work in both forms; same-Agent Fork currently requires a Git workspace.
- Share a shorter `cd2` Connection Code. It fixes the Deployment identity and endpoint but no longer embeds the Project ID; the Server stores only a hash of the opaque, time-limited capability.
- Use `coconet status` to issue a fresh Connection Code as any active member.
- Use one exit command: `coconet disconnect` revokes the current remote Membership first, then removes matching local bindings. The separate `leave` command has been removed.
- Keep the normal user CLI focused on `init`, `connect`, `status`, `disconnect`, and `uninstall`.

## Install or upgrade

```bash
npm install --global coconet@0.5.0
coconet version
```

The global npm launcher downloads only the signed native Client archive for the current platform from this exact GitHub Release, verifies its pinned SHA-256 and bundle manifest, and installs or upgrades the Runtime in the same terminal.

## Server upgrade note

Connection Code `cd2` and metadata schema 4 require the `0.5.0` Server. Upgrade a self-hosted Server before distributing the `0.5.0` Client. The migration preserves Projects, Memberships, Sessions, immutable Versions, artifacts, indexes, lineage, and audit records; legacy short-lived `cd1` connection tokens are revoked and should be replaced by running `coconet status` after the upgrade.

The Hosted endpoint at `https://api.coddis.work` already runs `coconet-server 0.5.0`.

## Integrity and platform support

Client assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Separate Linux Server operator archives cover both architectures. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the exact set.

macOS Runtime and Bootstrap binaries are Developer ID signed, hardened, timestamped, and notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

# Coconet v0.4.1

Coconet `0.4.1` adds a complete, safety-bounded user-level uninstall flow. Client, npm, and both Agent Plugins advance to `0.4.1`; the unchanged Server operator archives remain at `0.4.0`.

## Highlights

- Run `coconet uninstall` to remove the Coconet Plugin and Marketplace from Codex and Claude Code through their official Plugin CLIs.
- Verify that the Plugin is no longer installed or enabled before removing the native Runtime.
- Remove only exact Coconet-owned Agent cache and Codex Plugin, Marketplace, and Hook trust sections; preserve unrelated Agent configuration and native Sessions.
- Back up a changed Codex configuration before writing it, and stop safely if it changes concurrently.
- Preserve local Coconet project bindings, credentials, queues, and artifacts by default.
- Use `coconet uninstall --purge` to additionally remove Coconet-owned local state and macOS Keychain credentials without changing remote Project Membership.
- Keep the Runtime and local data when any Agent integration cannot be removed, avoiding loaded Hooks that point to a missing executable.

## Install or upgrade

```bash
npm install --global coconet@0.4.1
coconet version
```

The global npm launcher downloads only the signed native Client archive for the current platform from this exact GitHub Release, verifies its pinned SHA-256 and bundle manifest, and installs or upgrades the Runtime in the same terminal.

## Uninstall

```bash
coconet uninstall
npm uninstall --global coconet
```

Use `coconet uninstall --purge` in the first step only when local Coconet credentials, bindings, queues, and artifacts should also be deleted. Restart running Codex or Claude Code sessions after uninstalling so already-loaded integrations are released.

## Integrity and platform support

Client assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the exact set. macOS Runtime and Bootstrap binaries are Developer ID signed, hardened, timestamped, and notarized by Apple; bundled Node retains its upstream signature.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

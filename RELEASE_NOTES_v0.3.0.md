# Coddis v0.3.0

Coddis `0.3.0` makes shared team context available when the Coding Agent determines it is useful, while keeping automatic Hooks focused on Session synchronization.

## Highlights

- Agents can actively search the team's synced Sessions using a query and optional repository-relative paths.
- Search automatically recognizes useful symbols and error tokens contained in the query.
- Agents can inspect one fixed Session version, pull original Session artifacts for deeper local analysis, and fork a pulled same-Agent Session when explicitly requested.
- Prompt and tool Hooks no longer initiate team searches automatically; background Session synchronization remains automatic.
- Client, npm, Hosted Server, and self-hosted Linux Server operator releases now share version `0.3.0`.

## Install or upgrade

```bash
npm install --global coddis@0.3.0
coddis version
```

The npm package installs the signed native Runtime for the current platform. Linux Server operator archives are available separately in this GitHub Release.

## Integrity and platform support

Release assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the complete release set. macOS binaries are signed with Developer ID and notarized by Apple.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

# Coddis 0.1.0 Preview

This is the first public Coddis preview candidate for small-team Coding Agent Session collaboration.

## Included

- Versioned Session artifact synchronization and cross-member Search / Read
- Separate local Session Pull and same-Agent native Fork
- Codex and Claude Code Plugin, Skill, Hook, and MCP integration
- Hosted authority and self-hosted Server Preview with filesystem storage by default and optional S3-compatible storage
- User-level installation with verified, platform-specific archives
- Structured Runtime and Server logging without Session content in operational logs

## Platforms

- Client: macOS / Linux × `amd64` / `arm64`
- Server Preview: Linux × `amd64` / `arm64`
- Windows is not supported in this release

## macOS distribution

The Coddis Runtime and Bootstrap executables are Developer ID signed with hardened runtime and a secure timestamp. Both Intel and Apple Silicon submissions were accepted by Apple notarization with zero issues. The bundled Node executable retains its upstream Node.js Foundation signature.

## Assets

- `coddis-0.1.0-darwin-amd64.tar.gz`
- `coddis-0.1.0-darwin-arm64.tar.gz`
- `coddis-0.1.0-linux-amd64.tar.gz`
- `coddis-0.1.0-linux-arm64.tar.gz`
- `coddis-server-0.1.0-linux-amd64.tar.gz`
- `coddis-server-0.1.0-linux-arm64.tar.gz`
- Per-archive `.sha256` files, `SHA256SUMS`, and `release.json`

Use the checksum files from the same Release. Do not trust checksums copied from an unrelated URL or another version.

## Installation

The npm entry point will be published only after these GitHub assets pass a public-download installation check:

```bash
npx @coddis/setup@0.1.0
```

The GitHub-generated “Source code” archives contain only this public distribution repository and are not the private implementation source used for the release binaries.


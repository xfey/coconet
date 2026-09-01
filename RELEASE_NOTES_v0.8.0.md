# Coconet v0.8.0

Coconet `0.8.0` moves the canonical Hosted API to the Coconet product domain. Client, Server, npm, and both Agent Plugins advance together to `0.8.0`.

## Highlights

- Use `https://api.coconet.space` as the single canonical Hosted origin shown by `init`, `connect`, and `status`.
- Keep the existing Hosted Deployment identity, Projects, Memberships, immutable Session Versions, normalized indexes, artifacts, audit records, and S3-compatible backend unchanged.
- Encode the canonical Hosted origin through the existing compact Connection Code marker, preserving the 54-character Hosted code length.
- Keep self-hosted HTTPS DNS, IPv4, IPv6, and loopback fixture endpoint formats unchanged.
- Avoid a product compatibility layer for the retired development origin. Pre-release development bindings should be disconnected and recreated against the new Hosted origin.

## Install or upgrade

```bash
npm install --global coconet@0.8.0
coconet version
coconet agent list
```

The global npm launcher downloads only the signed native Client archive for the current platform from this exact GitHub Release, verifies its pinned SHA-256 and bundle manifest, and installs or upgrades the Runtime in the same terminal. Registered Agent integrations are resynchronized automatically after the Runtime upgrade.

Runtime and Agent integration upgrades do not remove native Agent Sessions. Because this is still a pre-release development environment without external users, existing bindings for the retired Hosted origin are intentionally not migrated by the product and should be recreated explicitly.

## Server upgrade note

Deploy the `0.8.0` Server before using the new default Client. Change only `advertised_endpoint` to `https://api.coconet.space`; do not recreate `deployment.json`, metadata, or the artifact backend. The Coconet Hosted service already runs `coconet-server 0.8.0` at the new canonical origin.

## Integrity and platform support

Client assets cover macOS Apple Silicon and Intel, plus Linux `arm64` and `x86_64`. Separate Linux Server operator archives cover both architectures. Each archive has a corresponding SHA-256 file; `SHA256SUMS` and `release.json` describe the exact set.

macOS Runtime and Bootstrap binaries are Developer ID signed, hardened, timestamped, and notarized by Apple. The bundled Node executable retains its upstream Node.js Foundation signature.

Windows is not currently supported. Product implementation source remains private and is not included in GitHub's automatically generated source archives.

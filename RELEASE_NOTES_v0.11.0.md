# Coconet 0.11.0

Coconet now explains processing failures consistently in the CLI and Dashboard, with suggested actions and correlation IDs for troubleshooting.

- Read suspended-task reasons and recent failure history with `coconet projection status` or the Dashboard's on-demand failure panel. Existing `projection retry` behavior remains available after repair.
- Inspect safe explanations for local synchronization failures through `coconet health --json`.
- Diagnose extraction, graph construction, Library checkpoints, and tagging through shared structured logs. Repeated failures are grouped; retained history may already have recovered.
- Keep business state committed when diagnostic logging fails. Project diagnostics follow existing membership checks and bounded retention: up to 256 events per project and 4096 across Server.

This release adds no model calls or real-time progress tracking. Generation audits, detailed usage statistics, manual corrections, and Library editing remain deferred for the lightweight MVP.

## Upgrade

Upgrade Server before clients. Metadata moves from schema 17 to 18; existing identities, fixed Session versions, Library sources, and Activity history are preserved. Existing data starts without reconstructed failure history.

Back up metadata, deployment identity, configuration, and the operator environment before upgrading. Rolling back requires the matching pre-upgrade metadata snapshot as well as the previous binary. API schema remains 1 with optional diagnostic fields.

```sh
npm install --global coconet@0.11.0
coconet version
```

Client, npm package, both Plugins, and Server are version 0.11.0. Archives cover macOS arm64/amd64 clients and Linux arm64/amd64 clients and Servers, with fixed checksums and a release manifest. macOS clients use Developer ID signing, hardened runtime, secure timestamps, and Apple notarization.

Validation covers repository checks, failure isolation, permissions, bounded retention, task leases, bbolt/PostgreSQL migration and restart, and desktop/mobile browser behavior. Large-scale performance and continuous team-use quality remain subjects for ongoing use.

# Coconet 0.9.0

Coconet now connects shared Sessions to project work and reusable Library snapshots.

- Work DAG: current and sealed work nodes, outstanding items, fixed evidence, and related-work discovery.
- Session Library: explicitly collect a fixed Session version, browse and search server-generated tags, and reuse original artifacts through same-Agent Pull / Fork. Every entry is linked to a sealed DAG checkpoint.
- Read-only Dashboard: sign in with an Account Key, inspect project work and source evidence, and copy local continuation commands.
- Durable processing: persisted extraction, graph and Library jobs support restart recovery and explicit retries. Summaries and labels use visible user/assistant text while original Session artifacts stay intact.
- Continuity: transferred Codex histories retain required ancestors, forked Sessions have independent identities and lineage, and Claude exit bookkeeping no longer prevents a valid SessionEnd from being recorded.

Client, npm, both Plugins, Server and Hosted use `0.9.0`. Platform archives, SHA-256 manifests, and signed, notarized macOS binaries are attached to this release.

## Upgrade

Install with `npm install --global coconet`. Existing registered Agent integrations are updated when the launcher installs the new Runtime. Keep Agent host runtimes current for native history transfer.

Self-hosted operators must back up metadata, deployment identity and artifact storage before upgrading. Metadata schema 4 migrates to 16 while preserving accounts, memberships, Sessions, versions, indexes, and artifact identity. Old short-lived Connection Codes are revoked and can be reissued. A schema rollback requires restoring the corresponding database snapshot as well as the old Server. Semantic work and Library features require an inference endpoint.

Summary quality remains model-dependent. Library collection or a sealed checkpoint does not certify correctness or replace reading the original evidence. Library management UI and broader sustained team-use validation remain future work.

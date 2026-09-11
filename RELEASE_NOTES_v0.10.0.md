# Coconet 0.10.0

Coconet now connects Activity history, the Work DAG, and the Session Library in the read-only Dashboard.

- Browse fixed checkpoints, successful Library collections, and registered Forks. Event snapshots retain their original source versions; new activity does not reorder pages already being read.
- Browse Library entries with collection notes, available topic tags, and fixed work summaries, then locate their DAG nodes.
- Share links to a specific event, Library entry, or Session version. Start with stage evidence and switch to the full fixed-version context when needed.
- Keep selected evidence when its source advances, and retain loaded views during transient refresh failures.

These views use existing facts and summaries without adding model calls. Checkpoint sealing and Library collection do not imply task completion. Fork lineage does not automatically create a Work DAG dependency. Library withdrawal and editing remain deferred.

## Upgrade

Upgrade Server before clients. Metadata moves from schema 16 to 17 and records an explicitly historical baseline for existing checkpoints, collections, and Fork lineage. Existing identities, Session versions, artifacts, and Library sources are preserved. Back up metadata, deployment identity, configuration, and the operator environment before upgrading. Rolling back requires the corresponding pre-upgrade metadata backup.

```sh
npm install --global coconet@0.10.0
coconet version
```

Open https://api.coconet.space/dashboard/ and sign in with your Account Key. Read and continuation commands still run locally; the browser does not control your Agent or working tree.

Client, npm package, both Plugins, and Server are version 0.10.0. Artifacts include macOS arm64/amd64 clients and Linux arm64/amd64 clients and Servers, with fixed checksums and a release manifest. The macOS Runtime and Bootstrap use Developer ID signing, hardened runtime, secure timestamps, and Apple notarization.

Validation covers the repository checks, transaction / pagination / permission tests, bbolt and PostgreSQL migration and restart, and desktop/mobile browser behavior. This release does not claim large-scale performance or continuous team-use validation.

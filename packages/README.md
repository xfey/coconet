# Shared implementation

- [workgraph](workgraph/README.md): pure graph state transitions and invariants.
- [graphstore](graphstore/README.md): device persistence, replicas and local search.
- [replication](replication/README.md): verified synchronization and temporary transfer.
- [contracts](contracts/README.md): wire structures, schemas and MCP tool definitions.

Other packages provide Agent integration, local inference, storage backends,
transport validation, logging and distribution. Application entry points live in
`apps/`; the top-level [architecture](../docs/architecture.md) explains their roles.

# Local runtime

The runtime is the device-side coordinator. Its CLI and MCP server use the same
project connections and local stores. `coconet connect` binds a working directory
to a deployment and project; Agent Hooks notify the runtime of subsequent updates.

`local_dag_observe.go` captures native session copies and detects effective branch
changes. Processing creates fixed inference tasks before invoking an Agent.
Retries and another device's takeover reuse the saved task inputs; a stale worker
cannot commit over a newer accepted result. Processing traces remain local.

Graph synchronization and content replication run independently. Accepted graph
references do not imply that every source is already available offline. Search
uses verified local text; read and fork keep the exact selected source identity.
Native history is never edited in place. Fork conversion runs on the requesting
device and preserves portable dialogue, not tool execution state or code changes.

See [architecture](../../docs/architecture.md), [MCP tools](../../docs/mcp-tools.md)
and [local persistence](../../packages/graphstore/README.md). Tests use isolated
roots and synthetic sessions; default tests must not invoke installed Agents.

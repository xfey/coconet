# Local runtime

The runtime is the device-side coordinator. Its CLI and MCP server use the same
project connections and local stores. `coconet connect` binds a working directory
to a deployment and project; Agent Hooks notify the runtime of subsequent updates.

`coconet server use <URL|official>` chooses the default for new connections;
`server list` shows known endpoints and the configuration path offline. The local
`config.json` contains only `default_server`. Existing folders and queued work
keep their pinned deployment and account; changing this preference moves no data.
See [server selection](../../docs/self-hosting.md#choose-a-default-server).

`local_dag_observe.go` captures native session copies and detects effective branch
changes. Processing creates fixed inference tasks before invoking an Agent.
Retries and another device's takeover reuse the saved task inputs; a stale worker
cannot commit over a newer accepted result. Processing traces remain local.

`coconet usage` reads background model input, output and cache tokens
across locally retained project/account/server stores. It requires no connected
directory or sign-in, runs no models and excludes ordinary Agent chats. See
[usage reporting](../../docs/usage.md) for coverage and retention semantics.

Graph synchronization and content replication run independently. Accepted graph
references do not imply that every source is already available offline. Search
uses verified local text; read and fork keep the exact selected source identity.
Native history is never edited in place. Fork conversion runs on the requesting
device and preserves portable dialogue, not tool execution state or code changes.

See [architecture](../../docs/architecture.md), [MCP tools](../../docs/mcp-tools.md)
and [local persistence](../../packages/graphstore/README.md). Tests use isolated
roots and synthetic sessions; default tests must not invoke installed Agents.

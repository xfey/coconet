# MCP tools

Coconet's MCP tools use the project connected to the Agent's current folder (or
its nearest connected parent). Joining a project in the Dashboard does not
connect a local folder. Run `coconet connect` in the work folder first; for your
own deployment, include `--server 'https://coconet.example.com'`.

Use `tools/list` for the current input schema. Each parameter includes its type,
constraints and usage guidance. Tools do not accept a `project_id` override.

| Tool | Input and limits |
| --- | --- |
| `coconet_health` | No arguments. Compact current-project processing, replica and search coverage for requested diagnosis or a blocked operation. |
| `read_project_work_graph` | Optional `limit`: 1–100, default 50; optional `cursor`. |
| `list_library_entries` | Optional `limit`: 1–100, default 50; optional `cursor`. |
| `search_work` | Required `query`: 1–4000 characters, not blank, at most 64 whitespace-separated literal terms. Optional `limit`: 1–20, default 10; optional `cursor`. |
| `pull_session` | Required `source`; optional `work_node_id`. `include_artifact` defaults to false; true also requires the node ID. |
| `add_session_to_library` | Required `request_id`, `work_node_id`, `source`; optional `note`, at most 1000 characters. |
| `remove_session_from_library` | Required `request_id`, `work_node_id`, `source`. Does not accept a note. |
| `fork_session` | Required `request_id`, `work_node_id`, `source`. Available to identified Codex / Claude Code hosts. |

Omit `limit` to use the default; zero is not a valid page size. A cursor is at
most 1024 characters. Copy `next_cursor` from the previous response, using the
same tool and query. If the query, graph or search index changes, restart without
a cursor. Do not raise the limit to request all results at once.

`source` is `{ "session_id": "…", "version": 1 }`: copy both fields from a
graph, Library or search result. The version must be a positive integer. Include
the corresponding node ID when selecting a node. These are Coconet references,
not native Agent session IDs. A source that is no longer available requires a
fresh selection; tools never silently substitute another version.

`request_id` is a stable unique string of 8–200 characters. After a timeout or
uncertain result, retry Fork / Library operations with the same ID and all
original arguments. Use a new ID for a new operation or changed selection.
Collection and Fork require an explicit user request; Library changes require
ownership of the selected Session.

## Recovering from errors

Known tool failures return `isError: true` and readable text, also mirrored in
`structuredContent`. Parameter errors include `status: "invalid_request"`,
`code: "invalid_arguments"`, field-specific `issues`, and `next_step`. For example,
`search_work` with `query: "CPT 数据生成"` and `limit: 100` identifies **limit** as
the problem, explains the 1–20 range and points to cursor pagination. Retry with
`limit: 20`, or omit it to use 10.

A valid call from an unconnected folder returns `status: "not_connected"` and
`code: "workspace_not_connected"`, with connection instructions. If both input
and connection are invalid, the parameter error also includes
`connection_status: "not_connected"` and instructions for both issues.

Execution failures provide a concise reason and a next step; verbose provider
output remains in local diagnostics. Missing temporary content, changed source references, stale cursors and
permission failures must not be interpreted as an empty search or success. For
unclassified Runtime failures, inspect `coconet status --verbose` and
`coconet diagnostics list` in the work folder before retrying.

Malformed JSON-RPC requests, unknown methods and unknown tool names remain
protocol errors with guidance. Tool-level errors are ordinary MCP tool results,
so hosts can forward them to the Agent for recovery.

## Background status

Successful reads and searches remain usable while unrelated background work is
pending or paused. Use the result's coverage and exact source to scope your answer.
A background capture failure does not identify the current conversation or prove
that a teammate's work is missing. Inspect `coconet_health` when the requested
operation is blocked or when the user asks about synchronization. It summarizes
only the connected project; `coconet projection status --json` and
`coconet diagnostics list` provide detailed local records for explicit diagnosis.

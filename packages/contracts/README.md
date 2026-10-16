# Contracts

Go structures define account, project, graph, content-transfer and MCP inputs and
outputs. JSON schemas under `schemas/` and the OpenAPI document under `openapi/`
are checked by contract tests. Runtime validation still enforces identity,
authorization, range and state-transition constraints beyond JSON shape.

[MCP tools](../../docs/mcp-tools.md) is the user-facing reference. It describes
argument limits, pagination, exact source selection and actionable failures.
Retired server-side processing/search endpoints return a local-runtime error;
clients perform those operations on member devices. Do not infer that every
historical schema remains an active server feature.

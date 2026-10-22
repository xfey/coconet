# Troubleshooting

Run diagnostics in the connected work directory:

```sh
coconet status --verbose
coconet agent list --verbose
coconet inference status
coconet diagnostics list
```

For background model tokens and cache usage across all locally retained projects,
run `coconet usage` from any folder. It shows the last 7 days, 30 days and all
retained history, with a model breakdown in each period. Missing counters are
unknown, not zero; see [Model usage](usage.md) for reporting details.

The Dashboard shows published progress, not a reliable live indicator of whether
a device is online or currently running a model. Check the source device's logs
when progress stalls. Use the Agent registration retry command shown by
`agent list --verbose` if a Plugin or Hook was not installed correctly.

For MCP failures, read the field-specific error and `next_step` before retrying.
For example, `search_work` accepts `limit` from 1 to 100 (default 10);
graph and Library listings also allow up to 100. More search results use `next_cursor`.
Joining in the browser alone does not connect the Agent's work folder. See
[MCP tools](mcp-tools.md) for all parameter bounds and recovery instructions.

An expired Agent login is separate from Coconet login. Renew the source Agent's
login in its own interface. Coconet normally uses that Agent's configured default
model; do not add server-side model keys to work around a device issue.

For a new self-hosted directory, include `--server` in the initial connection.
A project ID alone does not identify the deployment. If a folder is associated
with the wrong project, run `coconet disconnect` there, then connect again.
Disconnecting does not delete the native conversation history.

If a server operator disables Debug login, previous Debug browser sessions and
device tokens stop working. Reconnect with a normal account; do not reset the
server's Deployment identity to solve an account mismatch.

Only attach sanitized diagnostic excerpts to issues. Remove tokens, cookies,
signed URLs, personal paths and conversation text. For sensitive reports follow
[SECURITY.md](../SECURITY.md).

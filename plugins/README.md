# Agent plugins

Codex, Claude Code and Cursor plugins forward Hook events and MCP requests to the
local Coconet runtime. Host-specific manifests live under their respective
`coconet/` subdirectories. Each plugin carries its own license and collaboration
Skill. Plugins do not embed provider credentials or native Agent executables.

Registration and removal are implemented by the runtime. See `coconet agent
--help` and the [command reference](../docs/cli.md) for listing, repair and removal.
Shared Skill changes must remain consistent across the three integrations.

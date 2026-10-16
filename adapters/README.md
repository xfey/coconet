# Native Agent adapters

Codex, Claude Code and Cursor adapters discover sessions, parse effective native
history and create independent continuations. Shared interfaces live in this
directory; each host keeps its own format handling under its subdirectory.

Capture retains native content separately from normalized user/assistant text.
Native rollback changes the effective branch; an ordinary fork must not be
misclassified as a rollback. Cross-Agent continuation converts portable dialogue
locally into a new target-native session. Tool results, native execution state and
working-tree changes are not reconstructed as executable actions.

Synthetic fixtures exercise import/export, compaction, undo, malformed artifacts
and source identity. Claude's pinned-SDK history test requires installing the
build dependency described in [development](../docs/development.md); it only reads
synthetic history and never contacts a model provider.

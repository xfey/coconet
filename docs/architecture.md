# Architecture

Coconet has three parts: a local runtime, a Relay server and the Dashboard.

| Part | Responsibilities |
|---|---|
| Local runtime | Capture Agent updates; keep raw and normalized session replicas; run work extraction and relation analysis through the source Agent; search local text; read and continue selected sessions. |
| Relay server | Accounts and project membership, device credentials, accepted graph commits and checkpoints, bounded shared inference tasks, temporary content transfers, retention and Dashboard APIs. |
| Dashboard | Display published DAGs and Library entries, join projects, connect directories, share project links and collect sessions. It does not run models or search session bodies. |

## From a conversation to a graph

1. An Agent Hook notifies the local runtime of a conversation update. Native
   adapters parse the active branch and preserve the original source separately
   from normalized text.
2. The runtime creates a fixed inference task. Work extraction identifies stages
   and their source references; relation analysis considers the relevant graph
   context and proposes dependencies. Local validation and deterministic reducers
   enforce graph and reference invariants.
3. Accepted operations are committed to the Relay using concurrency checks. A
   published checkpoint is the Dashboard's consistent read boundary. Client
   retries keep request identity; stale workers cannot overwrite newer results.
4. Devices replicate the retained sources through temporary Relay transfers.
   Search uses local normalized text. Reading or continuing work follows the
   selected node's fixed source; native continuation creates a new local session. Codex, Claude Code and Cursor
   CLI can continue each other’s sessions: the client converts portable user and
   assistant text to the target format. Tool execution state is not transferred,
   and continuation does not restore the working tree.

There is no permanent project worker. A device preparing its own work may claim a
previous failed or expired task, execute the same fixed request with its own
Agent, and retain the original author's attribution.

Conversation undo changes the effective branch. Withdrawn progress and invalid
references are handled by the graph protocol; later reads must follow retained,
effective sources. Deterministic validation is separate from model judgment.

## Boundaries

The account store defaults to bbolt. Relay state is stored separately; content can
use local files or S3-compatible temporary object storage. Models and text search
run on member devices. The server still stores graph metadata and temporarily
holds shared content.

The implementation is organized under `apps/`, `adapters/`, `packages/`, `plugins/`
and `prompts/`. API schemas live under `packages/contracts/`. Synthetic tests cover
concurrency, retry, rollback, retention and offline recovery; they do not establish
an unlimited graph size or guarantee model judgments on every conversation.

## Project notes and manual updates

Project notes are ordered plain-text entries. The server enforces author-only edits and deletions, per-note optimistic concurrency and idempotent retries. Each change advances the project version; the Runtime synchronizes one combined text snapshot with author names and publication timestamps into an account/project-scoped local cache. Native session-start and user-turn Hooks announce changes, and the read-only `read_project_notes` tool reads that cache. Unsaved browser drafts are never published.

`coconet update` selects a stable client release, updates its npm launcher and verifies the Runtime archive before activation. Activation waits for in-flight work, including native continuation receipts. After `npm install -g coconet`, the next launcher invocation activates the packaged Runtime if it is newer, then continues the original command. Ordinary startup only compares locally installed versions and never downgrades. Pre-drain workers migrate after native handoffs finish, restoring persistent queues; a summary may retry once. Plugin content fingerprints avoid reconfiguring unchanged integrations and preserve disabled or removed plugins. New-version notices do not install anything automatically.

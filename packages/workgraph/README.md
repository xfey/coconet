# Deterministic local Work DAG

`types.go` defines versioned sources, per-Session progress, stage decisions, relation
decisions and immutable commits. `apply.go` validates and applies a commit to a
copy of a project state. It performs no I/O, clock reads, ID allocation or model
calls. Failed commits never change the input state. Retained sources and all
program-assigned identities are explicit, so saved commits can be replayed.

The stage transaction owns text, items and the Session boundary. The relation
transaction owns additive evidence-backed edges, its separate cursor and parent
sealing. Model decisions do not contain device IDs or project revision numbers;
the execution layer supplies those commit preconditions.

`apply_test.go` checks source advancement, separation of the two cursors, delayed
edges, foreign parent sealing, replay, conflicts, cycles and retention.

`retirement.go` adds an explicit replayable retirement transaction. Snapshot
counters remain monotonic; retained descriptors may be sparse. Latest and
processed boundaries, every unprocessed snapshot, node/edge sources and Library
nodes are protected. The owner may retire only consumed, unreferenced sources;
retired identities cannot be captured again. Callers must additionally protect
unfinished tasks and leases. This reducer does not delete any object files.

`checkpoint.go` validates sparse descriptors and complete unprocessed ranges
without iterating over historical version counters. State copies clone maps and
nested slices rather than serializing a second complete checkpoint. Capacity
fixtures distinguish metadata improvements from physical storage reclamation.

`rewrite.go` applies an authoritative native branch replacement atomically with
its new snapshot. It removes affected nodes and their incoming references,
restores per-Session items from small retained-node checkpoints, and marks the
old suffix sources superseded. Independent colleague work stays intact. Explicit
Library evidence can remain withdrawn, outside the current graph. `ActiveSources`
selects default search/read roots; `ReadableSources` additionally includes those
explicit bookmarks. Byte deletion remains governed by retention and leases.

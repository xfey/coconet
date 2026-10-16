# Device persistence

The bbolt graph store owns accepted state, immutable commits, pending proposals,
processing tasks and minimal audit records. Applying a commit and updating its
publication state is atomic. Model request/result archives are saved locally
before publication, separately from shared graph provenance.

Native and normalized content is immutable and hash verified. Native artifacts
use compressed chunks so appended snapshots reuse unchanged prefixes. A completed
replica requires its full verified closure; late downloads cannot resurrect a
retired identity. Cleanup only operates on owned copies, never Agent originals.

SQLite FTS5 is a separate, rebuildable text index. Search filters results against
currently readable graph sources, and pagination binds to the graph/index state.
The index never determines the source of a native fork.

Project archive export includes accepted graph and retained content, excluding
credentials and private model traces. Restore validates paths, sizes, hashes and
native reconstruction before publishing availability. It does not replace a live
connected graph with an unrelated archive. Tests cover transaction rollback,
reopen/replay, corruption, retirement races, indexed search and offline recovery.

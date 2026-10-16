# Project replication

Graph commits and content transfers are separate. The client validates ordered
receipts, base revisions and state digests before advancing its accepted state.
A conflict rebases unpublished work without pretending it was already accepted.
Checkpoints let new devices start at a verified boundary; recovery after server
history retention preserves pending local work and existing content.

Background workers replicate retained normalized and native source closures.
Every chunk and reconstructed artifact is verified. Explicit read/fork operations
keep the selected source identity and never silently switch to a newer version.
Receiving another device's content does not rerun its model inference.

Temporary transfers can use signed S3-compatible URLs without forwarding Relay
credentials or following redirects. A missing object can be repaired by another
authorized member holding the retained source. Requests wait when no holder is
available; graph identity remains unchanged. A cache hit alone never authorizes
cross-project transfer.

Accepted graph updates trigger checkpoint publication; idle synchronization backs
off. Native rollback removes superseded sources from default replication and
search, while explicit Library bookmarks retain their documented read scope.
See [data sharing](../../docs/data-sharing.md) for persistence boundaries.

# Data sharing and retention

Connecting a directory enables capture of subsequent Agent conversation updates
within its collaboration root. It does not upload the whole source repository or
automatically import all historical conversations. Continuing an existing session
can include earlier content from that session. Tools and source text may contain
sensitive material; members should understand what their sessions contain.

Member devices keep retained raw and normalized session replicas. Search reads
local normalized text; a selected source can be fetched when needed. Continuing a
session creates an independent local session rather than editing the original.

The Relay retains accounts, membership, device credentials, graph operations,
checkpoints and transfer metadata. Shared inference task inputs and transferred
content have bounded retention. With external object storage, session bodies are
temporary objects, while graph and source metadata remain available.

Local inference means invoking the Agent on a member device; its configured model
provider receives the inference input and charges according to that account's plan. A failed task may be processed by another
authorized member device using its provider.

Project IDs are joining capabilities for signed-in users. Opening a link alone
does not join; explicitly submitting its ID does. Account login and project
membership still govern reading and writing. Keep IDs within the intended team.

Deleting temporary objects is not a complete backup strategy. Server metadata
backups using an external content backend do not contain session bodies. Preserve
authorized member replicas or export project content with `coconet storage export`
when long-term recovery is needed. Same-machine backups do not protect against
loss of the entire machine.

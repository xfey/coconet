# Dashboard

Plain JavaScript modules, CSS and static assets are embedded in the Go server.
There is no frontend build service or browser-side model execution. The page reads
published graph checkpoints and subscribes to updates; intermediate commits do
not expose a half-published graph. Search and native session continuation belong
to the local runtime.

Follow [DESIGN.md](DESIGN.md) for bilingual text, shared controls, dialog hierarchy,
responsive layout and accessibility. `make npm-test` runs module tests; production
handler browser regressions are documented in [development](../../docs/development.md).
Vendor and font directories retain their upstream notices.

Shared project links show the project name and explain the shared progress and
sessions before asking new members to join. A signed-in, read-only preview exposes
only the project's ID, name and status; it does not grant membership or read access.
Unavailable project details can be retried, and joining stays disabled until they
load. Links do not identify an inviter. The same dialog then confirms membership
and offers project folder connection: **OK** closes it, while **How to connect a project folder**
opens the project's connection instructions. Joining from the sidebar uses the
same confirmation; existing members go straight to the project. Viewing the
instructions does not join again or authorize a device. Commands retain the
selected project and server address. Creating a project from the website opens
the connection guide directly; creating one during a CLI folder connection still
completes that connection. Closing a pending dialog prevents a late response from
opening the guide.

Both the sidebar and folder-connection join forms are labeled **Join with a shared link**, with
`https://coconet.space/...` as the placeholder. Invalid-link and missing-project
messages guide users to check the shared link.

Share offers one project link to copy. Connection instructions omit the separate
project-name caption and show a single `coconet connect` command by default,
including the selected project and the API address for self-hosted deployments.
The guide asks users to run the command in their project folder and reopen the
Agent in that same folder. When all connected folders share a name, that name
appears as an example. Empty, differing or unavailable directory records leave
the generic instruction in place; names do not restrict which folder can connect.
The **First time here?** disclosure contains installation and Hook guidance.
Open the guide from Share or the membership confirmation after signing in.
Hosted and self-hosted sign-in pages omit folder-connection guidance, and there
is no separate sidebar installation entry. Interface copy uses **project**
for the shared collaboration space and **project folder** for a device path.

Project notes appear as ordered, content-sized bubbles with basic Markdown lists,
bold, italic and inline code. Editing preserves the original text. Click **+ Post a note** to
write; leaving the input saves it. Members can read all notes and edit or delete
only their own, enforced by the server. Notes expand the panel naturally up to
the available canvas height. Drafts survive failed saves; conflicting changes
to the same note require explicit resolution. Agent reads combine published
notes into one ordered text snapshot with authors and publication timestamps.

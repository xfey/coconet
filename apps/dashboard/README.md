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
and offers local folder connection: **OK** closes it, while **How to connect a local folder**
opens the project's connection instructions. Joining from the sidebar uses the
same confirmation; existing members go straight to the project. Viewing the
instructions does not join again or authorize a device. Commands retain the
selected project and server address.

Connection instructions show a single `coconet connect` command by default,
including the selected project and the API address for self-hosted deployments.
The **First time here?** disclosure contains installation and Hook guidance.
Open the guide from Share, the membership confirmation, or the sign-in page;
there is no separate sidebar installation entry. Interface copy uses **project**
for the shared collaboration space and **local folder** for a device path.

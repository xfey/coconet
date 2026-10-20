# Getting started

Use macOS or Linux with Node.js 18.17+ and npm. Install and sign in to at least one
supported Agent: Codex, Claude Code or Cursor CLI.

## Connect a folder

Run these commands in the local folder where you work:

```sh
npm install -g coconet
coconet connect
```

Open the link printed in the terminal if the browser does not open automatically.
Sign in, then choose an existing project, join through a shared link, or create a
project. Confirm the folder connection and reopen your Agent. If it asks to allow
Coconet Hooks, approve them so conversation updates can be captured.

Share the project link with teammates. They can browse progress without installing
anything, or run `coconet connect` in their own folder and join the same project.
If you already know the project ID, you can pass it directly:

```sh
coconet connect coco_PROJECT_ID
```

Replace `coco_PROJECT_ID` with the complete ID from the project link. Project links
and IDs let signed-in users join, so share them only with intended collaborators.

For your own server, add its address:

```sh
coconet connect --server 'https://coconet.example.com'
coconet connect coco_PROJECT_ID --server 'https://coconet.example.com'
```

To make it the default for new connections, use `coconet server use URL`.
`coconet server use official` switches back; existing folder connections stay
with their original server. See [self-hosting](self-hosting.md) for server setup
and configuration details.

## Work together

Use your Agent normally; new conversation updates sync automatically. You can ask:

- “See what my teammates are working on.”
- “What did we decide about login, and who worked on it?”
- “Find my teammate’s session so we can finish the remaining work.”

The Agent can explore the work graph, search and read shared conversations, and
continue a session in the same or another supported Agent. Continuation creates
a new local session and leaves the source unchanged. It does not restore files
or synchronize code: use Git to keep the working tree in sync.

Existing history is not imported in full automatically. Continuing an old session
can include earlier content from that session. Use `coconet history start --agent codex|claude|cursor` to import this connected folder’s history. Preview and filtering are optional; batches advance automatically.

Work graphs use the executing Agent’s existing login, provider and default model;
background inference consumes that account’s quota. No extra model key or call
limit configuration is needed. Clients retain session replicas and search text
locally; the server coordinates sharing and temporarily relays session content.
See [data sharing and retention](data-sharing.md) for the full scope.

## Help and maintenance

```sh
coconet --help
coconet help connect
coconet history preview --help
coconet-server help init
```

The [command reference](cli.md) covers parameters, defaults and examples;
[MCP tools](mcp-tools.md) describes Agent-facing operations.
For connection problems or disabled integrations, see [troubleshooting](troubleshooting.md).
To disable an integration or uninstall Coconet, follow the
[installer guide](../installers/npm/README.md).

# Coconet 0.14.0

Coconet now uses a unified account across your devices. Hosted signs in with GitHub; private servers can use OIDC or administrator-issued Account Keys.

Run `coconet init` in a project folder. The browser handles login, project selection or creation, and approval to share that folder. `status` shows your account, project, local upload queue and Dashboard link. Invitations, profile editing, project membership and device access are managed on the web. Invitations remain valid for one hour.

Each device receives its own revocable credential. `disconnect` only unlinks the current folder; `logout` only signs out the current device. Independent Sessions from the same account and project appear together without merging their contents.

Advanced CLI options remain available: `init --no-browser`, `init --project ID`, `init --new`, `status --invite`, and `connect <code>`.

Upgrade the Server before the client. Server metadata moves from schema 19 to 20; back up metadata, deployment identity, configuration and environment first. Earlier accounts and Sessions are retained, but GitHub identities are not automatically merged with Git-profile accounts or granted their memberships. Use an invitation from an existing member when needed. Rollback requires restoring the matching database backup.

Client, npm, Agent plugins and Server: **0.14.0**. Four client platforms and two Linux server architectures are included. macOS binaries use Developer ID signing and Apple notarization.

Validation includes Go and process integration regressions, browser login and project setup, existing DAG / Library interactions, independent devices, authorization rejection and replay, and bbolt / PostgreSQL migration and restart tests.

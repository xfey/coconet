# Relay server

The server owns accounts, membership, device credentials, ordered graph receipts,
checkpoints, shared processing leases and temporary content-transfer coordination.
It serves the embedded Dashboard. It does not run model inference or search raw
session text. Members' runtimes provide those capabilities.

Authentication supports GitHub OAuth, OIDC and operator-created Account Keys.
Project IDs are joining capabilities for signed-in users. Browser requests check
origin and session state; device authorization uses a separate polling exchange.
The retired trial login has no HTTP route; a small startup migration revokes
previously issued trial credentials without deleting project ownership.

Account storage defaults to bbolt. Relay metadata and temporary content have
separate storage interfaces and capacity policies. Content may use filesystem or
S3-compatible storage. Never change an initialized backend in place. Keep stable
identity/configuration alongside metadata when backing up or restoring.

Tests cover normal authentication, project isolation, concurrent writers, leases,
retention, snapshot restore and malformed input. Browser harnesses use a loopback
fixture identity provider with the production handlers, without real accounts.
See [self-hosting](../../docs/self-hosting.md) and [storage operations](../../deploy/STORAGE.md).

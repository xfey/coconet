# Storage monitoring and backups

Copy `storage-check.py` and `backup.py` to `/usr/local/lib/coconet/`. Install
operator-reviewed copies of the corresponding `.example.json` files under
`/etc/coconet/`; never commit real config or credentials. Tune thresholds to the
host's disk size and application data paths.

Install the provided `coconet-storage-check.service` / `.timer` and optionally
`coconet-backup.service` / `.timer` units under `/etc/systemd/system/`.
Run each service manually and inspect the result before enabling its timer.
Journal messages are local; an external notification channel is a separate
operator integration.

## Online metadata backup

Online backup requires bbolt authority storage, external Relay content and no
legacy artifact inventory. Enable the private local operator socket:

```sh
COCONET_OPERATOR_SOCKET=/var/lib/coconet/operator.sock
```

The socket must remain inside the private directory containing the authority
database. The Server service user needs access to the backup directory. The
backup helper creates a consistent snapshot, restores it into an isolated
directory for verification, and records `restore_verified` in its status report.
Retention limits cover count, age and bytes; reserve free space for a new backup.

File-content deployments require a stopped server for a complete backup. Using
the same storage environment as the service:

```sh
coconet-server backup --config /var/lib/coconet/server.json --output /safe/backup/server.db
coconet-server restore --input /safe/backup/server.db --data-dir /safe/restore-check
```

Keep the database, object directories and completion manifest together and
private. The manifest contains Deployment identity material. External-content
backups retain metadata and temporary task inputs, not the object store's session
bodies. Account-store PostgreSQL deployments have different operator backup
requirements and are not covered by this bbolt restore workflow.

## Recovery boundary

Restore to a new directory and verify it before switching the service. Do not
run two writing servers with the same identity. Preserve OAuth / storage secrets,
TLS, proxy and systemd configuration separately. Copy completed backups to a
separate protected machine or storage service; same-host backup alone cannot
survive loss of that host. Encrypt off-host copies and limit access.

Members should use `coconet storage export` for durable project-body archives.
Temporary Relay objects expire; they are not permanent backup storage.

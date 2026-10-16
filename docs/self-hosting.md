# Self-hosting

Use a Linux Server bundle from the release page or build `./apps/server`. Clients
install separately with `npm install -g coconet`. The server does not need Codex,
Claude, Cursor or model-provider credentials.

## Initialize

Choose a client-reachable HTTPS origin, for example `https://coconet.example.com`.
The server may listen on loopback behind Nginx. Local HTTP is for loopback testing.

```sh
sudo useradd --system --home /var/lib/coconet --shell /usr/sbin/nologin coconet
sudo install -d -o coconet -g coconet -m 0700 /var/lib/coconet
sudo -u coconet /usr/local/bin/coconet-server init \
  --data-dir /var/lib/coconet \
  --advertised-endpoint https://coconet.example.com
```

Install the chosen binary at `/usr/local/bin/coconet-server` first. Keep the
generated Deployment identity, config and databases together; do not regenerate
the identity during upgrades.

## Login and HTTPS

Copy [server.env.example](../deploy/systemd/server.env.example) to
`/etc/coconet/server.env`, owned by root with mode 0600. For GitHub login configure:

```sh
COCONET_AUTH_PROVIDER=github
COCONET_AUTH_CLIENT_ID=YOUR_CLIENT_ID
COCONET_AUTH_CLIENT_SECRET=YOUR_CLIENT_SECRET
```

Register the OAuth callback as `https://coconet.example.com/v1/auth/callback`.
OIDC is also supported through the issuer / provider settings in the template.
Default key mode requires operator-created accounts; it is not public signup.

Install the [server unit](../deploy/systemd/coconet-server.service) and
[journal configuration](../deploy/systemd/journald@coconet.conf). Review the
[Nginx example](../deploy/nginx/coconet-server.conf), replace its domain and TLS
paths, and configure DNS / certificates before exposing the service. Trust
forwarded headers only from your controlled proxy's exact network addresses.
The supplied journal namespace requires systemd 245 or newer.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now coconet-server.service
curl --fail https://coconet.example.com/healthz
```

## Connect members

```sh
coconet connect --server 'https://coconet.example.com'
```

Create or join a project in that deployment. Share its `coco_…` ID or
`https://coconet.example.com/coco_…` link. The Dashboard includes `--server` in
copied directory-connection commands. Each connected directory remembers its
server; project IDs do not encode a server address.

## Storage and maintenance

Local bbolt and filesystem content are sufficient for a development deployment.
For temporary object transfers, configure the independent `COCONET_RELAY_S3_*`
variables before initializing Relay content. Legacy `COCONET_S3_*` variables do
not configure the new Relay backend. Do not change the storage backend of an
initialized Relay directory in place.

External content expires by application policy (168 hours by default). Do not
apply bucket-wide age-based deletion to the Relay prefix: active content can be
renewed without changing its creation date. Members need durable local replicas
or explicit archives for long-term recovery.

Install the [storage and backup tools](../deploy/STORAGE.md). Monitor whole-host
disk usage in addition to application quotas. Keep backups and credentials outside
the source checkout. Upgrade the server and cooperating clients together when
the connection or processing contract changes.

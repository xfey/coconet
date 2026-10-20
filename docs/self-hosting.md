# Self-hosting

Use a Linux Server bundle from the release page or build `./apps/server`. Clients
install separately with `npm install -g coconet`. The server does not need Codex,
Claude, Cursor or model-provider credentials.

## Initialize

Choose a client-reachable HTTPS origin, for example `https://coconet.example.com`.
New deployments listen on `127.0.0.1:7788` by default, behind Nginx.
Use `--listen HOST:PORT` to override this; upgrades retain the saved listen address.

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

## Trusted-network HTTP testing

For an isolated trusted network without TLS, initialize with `--listen 0.0.0.0:7788` and
`--advertised-endpoint http://coconet.example.com:7788`. Limit access to that
network. HTTP sends account keys and shared content without transport protection;
use HTTPS for public deployment. GitHub and OIDC login require HTTPS.

Use administrator-issued Account Keys for HTTP. With the server stopped, create
one using `coconet-server account create --config /var/lib/coconet/server.json
--name NAME`; keep the printed key private. Start the server and enter the key on
the login page. Each member should have their own key.

In the terminal used for Coconet and the Agent, run:

```sh
coconet connect --server 'http://coconet.example.com:7788'
```

The configured URL selects the protocol. Connected folders and Agent hooks use
the saved endpoint, including after opening a new terminal or restarting the runtime.

## Connect members

```sh
coconet connect --server 'https://coconet.example.com'
```

Create or join a project in that deployment. Share its `coco_…` ID or
`https://coconet.example.com/coco_…` link. The Dashboard includes `--server` in
copied directory-connection commands. Each connected directory remembers its
server; project IDs do not encode a server address.

## Choose a default server

For repeated connections to one deployment:

```sh
coconet server use https://coconet.example.com
coconet connect
coconet server list
```

`coconet server use official` restores the official default. `server use` only
saves a preference: it does not contact a server or sign in. `connect --server URL`
selects an endpoint for that connection without changing the default. Connected
folders keep their own server regardless of the current default; disconnect a
folder before connecting it to a different project or server.

`server list` prints the configuration path and known endpoints from saved logins
and folder bindings, without credentials. The editable `config.json` contains
only `default_server`. It lives in the existing Coconet configuration directory:
`~/Library/Application Support/coconet` on macOS, `$XDG_CONFIG_HOME/coconet` or
`~/.config/coconet` on Linux. An explicitly set `XDG_CONFIG_HOME` is also honored
on macOS for custom installations and isolated tests.

```json
{
  "default_server": "https://coconet.example.com"
}
```

Credentials remain in the credential store. DAGs, session replicas, indexes and
pending work are scoped to the deployment identity and account, with projects
inside that scope. Changing the default does not move or upload existing work.
The same client and Agent plugins can serve multiple deployments.

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


## Deployment quotas

`COCONET_MAX_PROJECTS_PER_ACCOUNT` sets the active-project limit per creator
(default `20`); `COCONET_MAX_MEMBERS_PER_PROJECT` sets active members per project
(default `50`). Both accept positive integers and take effect after restarting
the server. These are administrator settings; clients need only the server URL.
Reducing a quota preserves existing projects and memberships and affects new
admissions. Retrying an already accepted creation/join remains idempotent.
Project names accept 1–120 Unicode characters, including Chinese and emoji.

Request-rate limits are separate from those quotas. Defaults are centralized in
`apps/server/deployment_limits.go`: creation allows 20 requests per IP and 10 per
account per hour; joining allows 60 per IP per minute. Other authentication and
metadata limits remain unchanged. HTTP 429 responses identify the limited
operation and include the actual remaining window in `Retry-After`; clients
should wait before retrying. Shared-IP teams retain independent account quotas.

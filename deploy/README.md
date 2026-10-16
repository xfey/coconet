# Server deployment tools

Follow [self-hosting](../docs/self-hosting.md) for initialization, HTTPS, login and
client connections. These are generic examples; no live deployment configuration
or credentials are included.

- `systemd/`: Server, isolated journal, storage monitor and backup service units.
- `nginx/`: Reverse-proxy example; customize domain and certificate paths.
- `storage-check.py` / `storage-check.example.json`: read-only capacity checks.
- `backup.py` / `backup.example.json`: online metadata snapshots and restore verification.
- [STORAGE.md](STORAGE.md): operational boundaries and installation.

Run the Python tests with `make ops-test`. Model credentials belong on member
devices, not on the Relay.

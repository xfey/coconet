# npm installer

This package is a thin launcher for platform bundles. Installation selects the
matching release, verifies its manifest and checksum, and installs the local
runtime and plugins. Native Agent clients must already be installed separately.
Run `npm install -g coconet` to update the package. On the next `coconet`
invocation, the launcher installs its newer Runtime and continues the requested
command. Ordinary startup compares local versions; it never selects an update
from the registry or downgrades an installed Runtime. `coconet update` remains a
single-command alternative that updates both immediately.

Activation verifies the Runtime archive and waits for active work. Legacy
workers migrate automatically after active session handoffs finish; an
interrupted summary may retry from its persistent queue. Failed upgrades keep
the installed Runtime available and retry on the next launch. Plugin content
fingerprints avoid repeating unchanged setup and preserve disabled integrations.

The source package remains `private: true` to prevent accidental publication.
Release scripts prepare the distributable package with an explicit version and
target. Run `npm ci --ignore-scripts --prefix installers/npm` and `make npm-test`
for tests. Refer to the [CLI guide](../../docs/cli.md) for install, update and
uninstall behavior.

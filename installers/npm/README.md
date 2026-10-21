# npm installer

This package is a thin launcher for platform bundles. Installation selects the
matching release, verifies its manifest and checksum, and installs the local
runtime and plugins. Native Agent clients must already be installed separately.
Once a Runtime is installed, ordinary commands use it without requiring its
version to match the npm package. `coconet update` explicitly updates both,
verifies the Runtime archive and waits for active work before switching.
New-version notices do not install anything automatically. Plugin content
fingerprints avoid repeating unchanged setup and preserve disabled integrations.

The source package remains `private: true` to prevent accidental publication.
Release scripts prepare the distributable package with an explicit version and
target. Run `npm ci --ignore-scripts --prefix installers/npm` and `make npm-test`
for tests. Refer to the [CLI guide](../../docs/cli.md) for install, update and
uninstall behavior.

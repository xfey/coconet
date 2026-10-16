# npm installer

This package is a thin launcher for platform bundles. Installation selects the
matching release, verifies its manifest and checksum, and installs the local
runtime and plugins. Native Agent clients must already be installed separately.

The source package remains `private: true` to prevent accidental publication.
Release scripts prepare the distributable package with an explicit version and
target. Run `npm ci --ignore-scripts --prefix installers/npm` and `make npm-test`
for tests. Refer to the [CLI guide](../../docs/cli.md) for install, update and
uninstall behavior.

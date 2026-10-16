# Release assembly

`build-internal-release.sh` requires a clean checkout and creates local candidates
from `internal-versions.json`. `build-public-release.sh` prepares stable release
metadata and npm packages; publication and deployment are separate operations.
See [development](../../docs/development.md) for prerequisites and commands.

`build-release-artifacts.sh` builds macOS/Linux client bundles and Linux server
bundles, checks repeated unsigned Go builds byte for byte, and verifies downloaded
Node archives. The Claude SDK is locked under `dependencies/claude-sdk/`.
`collect_go_licenses.py` reads actual linked modules from each binary, collects
upstream terms and fails if notices cannot be obtained. Both bundle types carry
that inventory, in addition to Coconet's license and other bundled components.

A client bundle includes Runtime, Bootstrap, Node, the Claude helper/SDK and three
Agent plugins. A server bundle includes the server and generic operator examples.
Native Agent executables are installed separately. `public-target.json` identifies
the official distribution repository; forks must review destinations before
publishing. Signing and publishing credentials must stay outside the checkout.

Tests validate package metadata, manifest integrity, archive determinism, input
validation and license inclusion. `make check` includes these checks without
publishing or signing anything.

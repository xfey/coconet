# Development

Use Go 1.26.9 or newer, Node.js 24.22 or newer, npm, Python 3 and Git. Network access is
needed to download pinned Go / npm dependencies. No production credentials or
Agent login are needed for `make check`.

```sh
npm ci --ignore-scripts --prefix apps/release/dependencies/claude-sdk
make check
go build -o bin/coconet ./apps/runtime
go build -o bin/coconet-server ./apps/server
go build -o bin/coconet-bootstrap ./apps/bootstrap
```

`make check` runs npm / Dashboard tests, operation-script tests, Go unit / contract /
integration tests, vet and application builds. Installing the pinned SDK enables
the offline native-history adapter test; it does not call a model. Browser regression scripts under
`tests/integration/` additionally require Playwright and a compatible Chromium;
their environment variables are described at the top of each script. These tests
start an isolated production handler with generated fixtures, not a real account.

For SDK updates, also run the offline import and Fork round trips, including long
conversations and rewound branches. CI enables these with the installed SDK:

```sh
COCONET_TEST_CLAUDE_SDK="$PWD/apps/release/dependencies/claude-sdk/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs" \
  go test -count=1 ./adapters/claude
```

These checks use generated histories and temporary profiles without starting an
Agent or calling a model.

For example, install browser tools outside the source checkout:

```sh
npm install --prefix /tmp/coconet-browser-tools --no-save playwright@1.64.0
node /tmp/coconet-browser-tools/node_modules/playwright/cli.js install chromium
export COCONET_PLAYWRIGHT_MODULE=/tmp/coconet-browser-tools/node_modules/playwright/index.mjs
node tests/integration/account_browser.mjs
node tests/integration/published_graph_browser.mjs
```

On a Linux CI host, Playwright may also need its documented browser system
dependencies. `COCONET_BROWSER_EXECUTABLE` can select an already installed Chromium.

The Dashboard is plain browser modules and CSS embedded using Go. There is no
separate frontend compilation service. Follow [DESIGN.md](../apps/dashboard/DESIGN.md).

A bare runtime binary is useful for command development. A complete installed
client also needs the Node runtime, Claude helper / SDK, and Plugin files supplied
by the release bundle. To build all supported bundles from a clean source commit:

```sh
make internal-release
```

This downloads the pinned Node distributions and Claude SDK, builds the supported
platforms and writes candidates under `bin/`. It does not publish or invoke models.
macOS public distribution additionally requires the maintainer's Developer ID
signing and Apple notarization. Those credentials never belong in this repository.
Internal npm packages remain `private: true`; stable release preparation takes
explicit client and server versions through `apps/release/build-public-release.sh`.

Internal model replay tools, personal Agent profiles, old validation transcripts
and production deployment files are intentionally absent from this source tree.
For optional real-Agent testing, use an isolated profile and workspace and retain
only sanitized evidence. Do not call a paid provider from default CI.

Release assembly reads dependency identities from each compiled binary and copies
upstream Go licenses and NOTICE files to `licenses/go/`. The inventory contains
versions and file hashes, without build-machine paths. Missing notices fail the
build. Node and Claude SDK licenses are included separately.

Dependency checks used by CI:

```sh
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
npm audit --omit=dev --prefix installers/npm
npm audit --omit=dev --prefix apps/release/dependencies/claude-sdk
```

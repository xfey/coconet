# Tests

`make check` runs synthetic unit, contract, process, installer and operation tests,
plus vet and builds. Browser regressions use the production handlers with isolated
OAuth and project fixtures; see [development](../docs/development.md) for setup.

Default tests do not require credentials, model quota or original conversation
history. Native executables in process tests are inert stubs on a controlled
PATH. Optional real-Agent validation must use disposable workspaces and separate
profiles. Never modify a user's original history to create a fixture.

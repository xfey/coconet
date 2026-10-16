# Dashboard

Plain JavaScript modules, CSS and static assets are embedded in the Go server.
There is no frontend build service or browser-side model execution. The page reads
published graph checkpoints and subscribes to updates; intermediate commits do
not expose a half-published graph. Search and native session continuation belong
to the local runtime.

Follow [DESIGN.md](DESIGN.md) for bilingual text, shared controls, dialog hierarchy,
responsive layout and accessibility. `make npm-test` runs module tests; production
handler browser regressions are documented in [development](../../docs/development.md).
Vendor and font directories retain their upstream notices.

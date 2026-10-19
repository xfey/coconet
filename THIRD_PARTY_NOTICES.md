# Third-party components

MIT applies to Coconet-owned code. It does not replace the following licenses or
the licenses of dependencies downloaded during a build.

| Component | Source / version | Terms and distribution |
|---|---|---|
| ELK / elkjs | elkjs 0.12.0 | EPL-2.0. Unmodified JS, license and source links are retained in [vendor](apps/dashboard/assets/vendor/README.md). |
| Outfit Brand subset | Outfit 1.100, modified subset | SIL OFL 1.1. Copyright, license, renamed subset details and hashes are in [fonts](apps/dashboard/assets/fonts/README.md). |
| Agent marks in README illustrations | LobeHub Icons, pinned source revision | MIT. Upstream icon paths, source hashes and the license are retained in [agent marks](docs/assets/agent-marks/README.md). Marks identify supported clients; they do not imply endorsement. |
| Node.js | Release build pins 24.22.0 | Node and bundled third-party terms. The release builder copies the upstream LICENSE with the runtime. |
| Claude Agent SDK | package lock pins 0.3.241 | Copyright Anthropic PBC; use subject to [Anthropic's applicable agreements](https://code.claude.com/docs/en/legal-and-compliance). Not covered by Coconet's MIT license. SDK code is downloaded during packaging, not vendored in this source tree; its license is copied into runtime bundles. |
| npm tar | Installer lock pins its dependency tree | Each package retains its upstream license. Installed by npm, not vendored here. |
| Go modules | [go.mod](go.mod), [go.sum](go.sum) | Modules retain their upstream licenses. Release bundles include `licenses/go/` with a versioned inventory and upstream licenses / notices for the modules linked into their binaries. |

The release scripts collect linked Go module licenses and preserve the pinned Node
and Claude SDK license files. The
embedded server assets include the ELK and font licenses. New third-party code or
binary distributions require an updated notice inventory; an MIT source license
does not imply that an upstream service or SDK is available under MIT.

Codex, Claude Code and Cursor CLI executables are installed separately by users.
Coconet adapters and plugins do not redistribute those clients or change their
service, account or model usage terms.

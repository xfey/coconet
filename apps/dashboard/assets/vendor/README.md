# Dashboard third-party code

ELK Layered runs in a same-origin classic Worker. No package install or CDN is
required when running the Server. Coconet's integration is in the adjacent
`graph.mjs` and `layout-client.mjs` files.

- Package: `elkjs` 0.12.0, https://www.npmjs.com/package/elkjs/v/0.12.0
- Vendored files: `lib/elk-api.js` and `lib/elk-worker.min.js`, copied without
  modification as `elk-0.12.0.api.js` and `elk-0.12.0.worker.min.js`.
- License: Eclipse Public License 2.0; the full text accompanies the distribution
  in [elk-LICENSE.md](elk-LICENSE.md), also served by the Dashboard.
- The ELK code is available in source form under the EPL-2.0:
  https://github.com/kieler/elkjs/tree/ff5771d7165445c42c408bb8a090c8035272218c
  and https://github.com/eclipse-elk/elk (the upstream layout engine).
- npm package: https://registry.npmjs.org/elkjs/-/elkjs-0.12.0.tgz
- npm integrity: `sha512-YZcKynxVxYoKIOEpywEPwCFdg+BTbxQRNf3pbwdDCvc8O3kQD8bmIwSxKU1eOTVc4Xo+VG9Te+575mlfvOrhEQ==`
- API SHA-256: `ccca46175c05bbd280b5de6c9853103711e52c666a101f145b717e345363e24d`
- Worker SHA-256: `e4f5c51c6f2fd564bb64e52e81c4a85a316ef2f24f8b90e1db5eb886c765b030`

The `.js.gz` file is the same JavaScript compressed using Python
`gzip.compress(content, mtime=0)` (461,595 bytes; identity: 1,595,334 bytes).
Only the exact versioned Worker URL uses immutable caching and optional gzip.
The application modules and this notice use `no-store`.

To upgrade, replace the unchanged upstream bundle, license and gzip companion;
update the versioned URLs, provenance and checksums; then run layout, static asset
and browser acceptance tests. Do not change the bytes behind an immutable URL.

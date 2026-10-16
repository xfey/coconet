# Repository collaboration

- Keep changes focused and preserve unrelated work. Commit independently verified stages.
- Run `make check`; update documentation for changed behavior.
- Dashboard changes follow `apps/dashboard/DESIGN.md`, including bilingual text and shared controls.
- Use synthetic data in tests. Real-Agent verification needs an isolated profile, disposable workspace and explicit scope; never modify original histories.
- Do not commit credentials, local runtime state, production backups or private conversation samples.
- Publishing artifacts or changing a live deployment is separate from editing source.

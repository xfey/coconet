# Coconet 0.12.0

DAG stages now group work around a coherent accepted goal, so related discussion, implementation, testing, documentation, and corrections can update one stage. Independent goals and explicit Session Library checkpoints remain distinct.

Historical initialization uses a dedicated overview that samples requests and conclusions across the Session. A new negotiated Runtime capability supplies optional native parent hints; the Server checks exact content prefixes against fixed sources in the same import batch before excluding inherited work. Existing Sessions, immutable Versions, Library entries, and published DAGs are preserved; upgrading does not rebuild an existing graph.

Self-hosted operators can set `COCONET_INFERENCE_BOOTSTRAP_REASONING_EFFORT=low` to control initial graph construction independently of other inference tasks. Without this setting, Bootstrap inherits the general reasoning effort. Metadata remains schema 18.

Validation includes package and integration checks, replay of real conversation evidence, and repeated historical graph construction. Semantic summaries and optional upstream edges can still vary; final ordinary-stage replay coverage is partial, and large bbolt imports retain full-state storage costs.

Update the client:

```sh
npm install --global coconet@0.12.0
coconet version
```

Restart existing Agent sessions to use the updated integration. macOS / Linux client archives and separate Linux Server archives are distributed here. This repository contains public release documentation, not product implementation source.

# Inference prompts

Work extraction identifies meaningful stages from the effective conversation;
relation analysis proposes evidence-backed dependencies against selected graph
context. Prompt inputs contain task content and relevant work references, while
execution identities and commit preconditions are supplied by code.

Model output is a proposal. Schema validation and deterministic graph reducers
reject invalid sources, cycles, stale revisions and other inconsistent changes.
Fixed shared tasks preserve their inputs across retries and device takeover.
Changes here should be tested with synthetic cases and representative isolated
sessions; successful JSON parsing alone does not establish graph quality.

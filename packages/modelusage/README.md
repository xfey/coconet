# Model usage

Normalizes native Agent usage receipts and aggregates local background processing
by project, model, task or date. Missing counters remain unknown. Input includes
cache reads/writes; reasoning is part of output. Claude auxiliary models are
included without double counting top-level usage. Codex cumulative totals are
counted once per attempt. Reported costs are CLI estimates, not invoices.

This package has no I/O or inference side effects. Graphstore persists normalized
counters independently of diagnostic body retention; Runtime exposes the
[usage command](../../docs/usage.md).

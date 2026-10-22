# Model usage

Run from any directory:

```sh
coconet usage
```

The command always shows **the last 7 days, last 30 days and all retained
history**. It needs no options, connected folder or sign-in. Each section lists
models with processing attempts, input/output tokens, cache reads/writes and a
total row. Numeric columns align right, long model names wrap, and narrow
terminals use a compact block per model.

## What the numbers mean

- Covers Coconet background processing on this device across all locally retained
  projects, servers and accounts, including disconnected projects. Ordinary Agent
  conversations and work executed on other devices are excluded.
- Input includes cache reads and writes.
- Failed attempts and retries remain included when usage was reported. Attempt
  count does not guarantee the request reached the model. Auxiliary models are
  included when reported; one attempt may appear under multiple models.
- Totals add reported values and ignore missing fields, without extra markers.
  `—` means no values were reported for that field; `0` means a reported zero.

Usage may be absent when startup fails before a model request, the Agent does not
report a field (such as cache writes), or a process ends without its final
receipt. Missing usage is not evidence that a successful call was lost. Retained
receipts are parsed when available; unavailable counts cannot be reconstructed
accurately from conversation length.

Time periods use attempt start time. New normalized counters survive diagnostic
body cleanup; already-pruned historical usage cannot be recovered. Deleting the
local store also removes its usage history.

The command reads a running worker through its authenticated local endpoint or
opens a stopped database read-only. It starts no workers or model requests. An
older running worker may need `coconet update`; unreadable stores produce partial
totals, a warning and exit status 1.

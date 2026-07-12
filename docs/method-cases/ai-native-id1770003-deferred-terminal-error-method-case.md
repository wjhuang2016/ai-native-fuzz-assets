# Method case: deferred terminal errors must dominate success

Remote `found_bug`: `id1770003`, confirmed high.

## How the LOOP found it

This candidate came from current source, not a PR finding. A source scan looked for terminal
`Close`/`Flush` actions executed in defer blocks where errors were logged but excluded from the
public result. `ProcessChunk` was admitted only after tracing `EngineWriter.Close` to the final
private-buffer flush and tracing nil to later engine import and task success.

## Selector S38

`DEFERRED_TERMINAL_ERROR_DOMINATES_SUCCESS`

The useful question is not "is Close called?" It is: if Close is the last durability boundary,
does its failure still own the public terminal result? A defer can provide reachability while still
losing error and state ownership.

## Minimal matrix

| Code | Close fault | Terminal result | Row/index oracle |
| --- | --- | --- | --- |
| current | none | success | 3/3, ADMIN green |
| current | index pre-flush error | success | 3/0, ADMIN 8223 |
| named-return counterfactual | same error | error | 0/0, ADMIN green |

The local mock matrix first proved exact error loss. The live matrix then proved the severe C3
consequence with a real import task and real PD/TiKV.

## LOOP improvement

After locating cleanup/finalizer coverage, add a terminal-result dominance pass:

1. Name the final action that transfers private state to the next durable owner.
2. Inject one error at that action, after normal work but before transfer.
3. Jointly observe API status, task status, durable artifacts, and semantic consistency.
4. Change only error ownership; rerun the same fault at the same altitude.
5. Treat logs and metrics as observers, never as proof that the error was handled.

This closes a blind spot in the earlier terminal-action selector. "Every Close was reached" is not
enough when the code can still publish success after a Close failed.

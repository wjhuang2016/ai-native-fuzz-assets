# Method case: a deferred terminal error must own the return slot

Remote `found_bug`: `id2130003`, confirmed high.

## Independent discovery

This hit did not come from a PR review or historical finding. The current-source retry-side-effect
scanner nominated `deleteKeysWithRetry`, then an owner trace reached `deleteBufferedKeys`:

1. `RunWithRetry` treats a nil callback error as completed work.
2. `deleteBufferedKeys` stages every delete and commits inside a defer.
3. The defer assigns `txn.Commit(ctx)` to a local `err`.
4. The function has an unnamed `error` result and ends with `return nil`.

The code checked **P**: the delete loop returned without an earlier error. It then assumed **Q**:
the transaction commit succeeded. Go's return semantics break that implication: `return nil`
fixes the unnamed return value before deferred functions run, so changing the local `err` cannot
change the public result.

## Selector

`DEFERRED_TERMINAL_ERROR_RETURN_SLOT_OWNERSHIP`

High-signal source shape:

- a terminal `Commit`, `Close`, `Flush`, or publish action runs in a defer;
- its error is assigned to a local variable;
- the enclosing function has no named error result, or the defer writes a shadowed variable;
- nil/success lets a retry owner or task owner publish completion.

This refines `DEFERRED_TERMINAL_ERROR_DOMINATES_SUCCESS`. A terminal call can be reached and its
error can even be assigned, yet still have no ownership of the return slot.

## Minimal matrix

| Code | One transient Commit fault | Terminal result | Access-path oracle |
| --- | --- | --- | --- |
| current | first conflict-delete Commit rolls back | finished | PRIMARY/unique/secondary = 2/1/2, ADMIN 8223 |
| current, same process | fault already consumed | finished | 1/1/1, ADMIN green |
| named-return counterfactual | same first Commit fault | one retry, then finished | 1/1/1, ADMIN green |

The input contains two duplicate primary-key rows, a third row sharing their unique key, and one
normal row. Correct capture semantics remove the three conflicted rows and retain only the normal
row. Current code reports one imported row while physically retaining an extra record and secondary
index entry with no matching unique-index entry.

## Why this worked

The strongest oracle was not the function error alone. It joined:

- task terminal status and imported-row summary;
- exact PRIMARY, unique-index, and secondary-index row sets;
- `ADMIN CHECK TABLE`;
- retry observation at the commit owner.

A one-shot retryable fault was especially discriminating. Current code never reached retry because
the deferred error missed the return slot. Naming the return slot exposed the same error to the
existing retry loop, whose next attempt completed the deletion and restored consistency.

## LOOP improvement

Add a return-slot ownership pass after locating terminal actions:

1. Resolve the function's actual result slots, including named results and shadowing.
2. For every deferred terminal call, trace whether its error can mutate the returned error slot.
3. Inject one transient fault before durable publication, not after it.
4. Require a no-fault same-process control and an exact return-ownership counterfactual.
5. Lift to the highest consumer with status plus durable semantic oracles.

Also refine retry-side-effect scanner negatives:

- **overwrite-only outputs** are low priority when every attempt re-reads the authoritative remote
  value and overwrites captured scalars before use;
- **attempt-entry reset plus fixed source replay** is low priority when buffers/counters reset and
  the outer frontier advances only after a successful attempt.

These rules retired the auto-ID allocator and nonpartition DDL backfill candidates before execution,
leaving the return-slot defect as the first admitted C3 target.

## Stop rule

Do not enumerate more IMPORT conflict shapes, batch sizes, index types, or retryable error strings.
Reopen only for another terminal owner, a different return-slot/shadowing mechanism, or evidence
that an error-visible failure still publishes an independently severe state.

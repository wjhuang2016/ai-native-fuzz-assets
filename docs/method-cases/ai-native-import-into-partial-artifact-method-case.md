# Method Case: irreversible sibling commit must dominate terminal cleanup

## Why the earlier screen missed it

The first terminal-action analysis noticed that `ImportSelectedRows` could return before a later
engine terminal action. It then found the deferred `closeAndCleanupEngine` calls and retired the
candidate as covered.

That owner proof was locally correct but incomplete. It answered whether every engine eventually
closes, but not whether another sibling artifact had already crossed an irreversible durable
boundary. Here the data engine is already imported into TiKV. Cleaning the local index engine is
not recovery; it destroys the remaining material needed to complete the table.

## Improved selector

`SIBLING_ARTIFACT_PRECOMMIT_ATOMICITY`

For every operation that owns multiple durable artifacts:

1. enumerate each artifact's states: `open -> prepared/closed -> durably imported -> cleaned`;
2. mark the first irreversible transition;
3. prove all sibling preparation and validation dominates that transition;
4. after the transition, require a retry/repair owner for every remaining terminal error;
5. treat cleanup as safe only when it cannot discard the sole recovery source.

The proof obligation is:

```text
P: artifact A is ready and imported
Q: every sibling artifact can still be finalized and imported
F: publish A before proving Q; on sibling error, clean local state and return
missing dimension: irreversible A cannot be rolled back, while cleanup destroys B recovery state
```

## Small matrix

| Fault altitude | Statement | Table scan | Forced index | ADMIN CHECK |
|---|---:|---:|---:|---:|
| no fault | success | 3 | 3 | GREEN |
| before data import | error | 0 | 0 | GREEN |
| after data import, before index close | error | 3 | 0 | RED 8223 |

Only three cells were needed. The before/after irreversible-boundary pair is more informative than
enumerating many filesystem or TiKV error types.

## LOOP improvement

```text
source early-return shape
  -> owner/finalizer dominance proof
  -> irreversible-boundary ledger
  -> recovery-source retention proof
  -> C3 state oracle
  -> minimal altitude matrix
```

A retired candidate may be reopened only when a new consequence dimension invalidates its old
retirement proof. The new dimension here is durable artifact ordering, not another Close variant.


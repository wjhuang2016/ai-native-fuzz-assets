# Method case: derived execution context must be keyed or rebuilt

## Why this target was found quickly

The starting question was not "which PREPARE tests are missing?" It was a proof obligation:

> If code observes P and reuses a cached object, which derived semantic state does it assume is
> still represented by P?

Current-source fast-path search exposed a session-level PREPARE dedup cache. The key inventory was
small and explicit: SQL, charset, collation, database, SQL mode, and schema version. The copied
payload inventory included `SnapshotTSEvaluator`, whose producer also reads session
`ReadStaleness`. That context owner was absent from the key.

## P, Q, and fast path

- P: SQL text, parse context, database, and schema version match.
- Q: all derived prepare-time execution semantics are reusable.
- Fast path: reparse and preprocess, skip full Build, then copy selected fields from the cached
  `PlanCacheStmt`.
- Broken proof: fresh Preprocess computes the current evaluator, but the fast path overwrites it
  with the old cached evaluator.

## Small matrix

| Cell | Old prepare context | New prepare context | Dedup | Result |
| --- | --- | --- | --- | --- |
| RED | `read_staleness=-1` | cleared, row updated to `2` | ON | returns old `1` |
| GREEN | same warm-up | cleared, row updated to `2` | OFF | returns `2` |
| GREEN | same RED schedule | fresh evaluator field | ON | returns `2` |

The matrix changes one semantic owner at a time. It does not enumerate SQL shapes, staleness
durations, or client libraries.

## Strong oracle

The rowset is the oracle. Reading cache-hit metrics or inspecting the copied function pointer would
only prove trigger reachability. The decisive evidence is that a new prepared statement returns a
historical value after the session explicitly returned to latest reads, while the identical SQL at
the same time returns the current value when only dedup is bypassed.

## Method improvement

Promote `DERIVED_EXECUTION_CONTEXT_MUST_BE_KEYED_OR_REBUILT`:

1. Inventory the cache key.
2. Inventory copied derived fields, not only raw inputs.
3. Trace every derived field to all session/config/time/identity producers.
4. For a missing owner, vary only that context while keeping payload identity fixed.
5. Compare fast-path hit against same-payload bypass.
6. Replace only the derived owner for counterfactual GREEN.

A particularly high-signal source pattern is "fresh analysis is performed, then a cached derived
field overwrites its result." It narrows both the candidate and the counterfactual before any large
test matrix is built.

## Provenance boundary

The candidate came from current-source cache/fast-path proof obligations. No PR review, issue,
fix, or history generated or ranked it. Asset and upstream searches happened only after local RED
and served only to deduplicate the independently proven root.


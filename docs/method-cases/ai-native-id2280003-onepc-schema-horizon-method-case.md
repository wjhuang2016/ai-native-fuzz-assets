# Method case: id2280003 1PC schema-validation horizon

## Starting proof obligation

The source pass looked for a fast path whose validation proves a time-bounded fact before an
irreversible atomic operation:

```text
P: schema is valid at V, before prewrite
Q: schema is valid when 1PC atomically applies at H
F: related DDL can finish in (V, H], and no downstream owner revalidates Q
```

TiDB creates a delta `SchemaChecker` when MDL is off. client-go calls it in
`calculateMaxCommitTS`, exposes an existing hold point immediately afterward, and returns directly
after successful 1PC prewrite. The ordinary 2PC sibling obtains its actual commitTS and checks the
schema again. That source contrast compressed the search to one protocol bit and one timing window.

## Small matrix

| Protocol | DDL in validation-to-prewrite window | Commit order | Consequence |
| --- | --- | --- | --- |
| 1PC | `ADD INDEX` | DML commitTS > DDL FinishedTS | RED: table row exists, index row absent, ADMIN CHECK fails |
| 2PC | `ADD INDEX` | DML commitTS > DDL FinishedTS | GREEN: TiDB retries, table/index rowsets match |
| 1PC | `TRUNCATE TABLE` | DML commitTS > DDL FinishedTS | RED: INSERT succeeds, current table is empty |
| 1PC | no related DDL | n/a | GREEN baseline |

Async commit remained enabled in the strongest live RED; the observed terminal mode was still
`1pc`. This proves the bug is not an artifact of disabling the adjacent fast protocol.

## Oracle correction that mattered

The first local oracle said "a successful INSERT must be visible after TRUNCATE." That was too
strong because the INSERT and DDL overlap; serializability permits the DML to commit logically
before the DDL even if its response arrives later.

Before promotion, the oracle was repaired to compare DML `commit_ts` with the DDL history
`FinishedTS`. Only `commit_ts > FinishedTS` admits the claim that the mutation used an obsolete
schema. The real TiKV RED satisfied that ordering. `ADD INDEX` then supplied a second, purely
physical coherence oracle: table scan versus forced-index rowset plus `ADMIN CHECK TABLE`.

This becomes a general rule:

```text
For overlapping operations, wall-clock invocation/response order is not a serialization oracle.
First prove logical order, then judge state against the owner active at that order.
```

## Why the method worked efficiently

1. It started from a proof gap between validation time and irreversible apply time, not from a
   broad DDL or transaction feature list.
2. The source provided a safe sibling: 2PC performs the missing commitTS-based check.
3. The existing `beforePrewrite` point controlled only the uncovered interval; it did not mock the
   schema validator, DDL result, TiKV commit result, or final oracle.
4. The highest consumer was selected before expansion: newly published index keys and replacement
   table identity, both capable of persistent wrong data.
5. The first RED remained provisional until real TiKV supplied the actual 1PC commit timestamp.

## Selector improvement

Add `VALIDATION_HORIZON_COVERS_IRREVERSIBLE_APPLY`:

1. Identify validation point `V`, irreversible apply point `H`, and every fact consumed at `H`.
2. Compute which facts can change in `(V,H]`.
3. Trace enforcement owners at `H`; reject candidates protected by version, lock, CAS, or downstream
   revalidation.
4. Build `fast path / safe path` and `change before / inside / after window` cells.
5. Require a logical-order oracle for overlapping actors.
6. Rank only consequences reaching row identity, index keysets, predicates, commit outcome, or
   terminal truth.

Stop after one root per uncovered horizon. Other DDL types, row counts, and delay values are blast
radius unless they change the validation owner or irreversible consumer.

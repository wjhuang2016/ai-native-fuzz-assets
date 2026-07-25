# id3060003: filtered batches must close their dependency graph

Status: confirmed high severity / critical data-integrity consequence on official BR and real
TiKV.

## Proof obligation

```text
P: the object was selected as part of an internal restore batch.
Q: every dependency required by the object's published semantics is in that batch.
F: the selector proves only P, while the consumer disables validation based on Q.
```

For BR:

```text
filterRestoreFiles selects child c
BRIECreateTables disables ForeignKeyChecks
CreateTables publishes c and its FK metadata
physical restore writes c rows
checksum checks c, not the c -> p dependency
```

## Small matrix

| Path | Selected graph | Oracle | Verdict |
| --- | --- | --- | --- |
| Ordinary DDL | child only | error 1824 | GREEN reference |
| BR table restore, run 1 | child only | success + orphan + invalid INSERT succeeds | RED |
| BR table restore, run 2 | child only | same durable state | RED |
| BR database restore | parent + child | zero orphans + invalid INSERT fails 1452 | GREEN |

No schedule search, concurrency, or fault injection was needed. The public selector itself creates
the premise mismatch.

## Strong oracle

The restore terminal and checksum are only the first observer. Close the result across:

1. referenced-table existence;
2. persisted FK metadata;
3. restored row referential closure;
4. the first future invalid write with checks enabled;
5. a dependency-closed restore from the same backup.

`ADMIN CHECK TABLE` remains green in the RED because it validates physical row/index structure, not
cross-table semantics.

## Selector

```text
candidate = public partial-object filter
            intersect selected metadata references an excluded object
            intersect internal consumer bypasses validation for batch ordering or restore
            intersect terminal validation inspects only selected artifacts
            intersect published state or future writes require the missing dependency
            minus dependency expansion, explicit rejection, or fail-closed runtime enforcement
```

Use the name `FILTERED_BATCH_MUST_CLOSE_DEPENDENCIES`.

## Why this worked

The earlier future-sibling selector tested a batch whose later member could disappear after
admission. This case removes concurrency entirely: the sibling is excluded at selection time, but a
downstream helper still acts as if the whole graph was selected.

The high-yield source pattern is:

```text
selector checks membership
consumer disables a safety check because "this is a batch"
validator checks each selected artifact locally
```

That shape converts a graph invariant into unrelated per-object checks.

## Loop improvement

For every partial restore, import, export, migration, cleanup, or batch API:

1. enumerate reference edges in selected object metadata;
2. compare exact selection with transitive dependency closure;
3. find validation bypasses justified by batch or internal execution;
4. use ordinary user DDL as a fail-closed reference;
5. observe both existing dependent artifacts and the first future consumer;
6. use the same input with the closed graph as GREEN;
7. keep local checksums and structural checks as secondary oracles.

This selector should transfer to views, sequences, placement policies, resource groups, privileges,
statistics, cached-table side rows, restore manifests, and multi-object cleanup.

## Stop rule

Count one root per selector and validation-bypass boundary. More FK actions, child tables, rows,
filter syntax, storage backends, and schema names are blast radius. Reopen only when another
dependency type reaches a different irreversible consumer or uses a distinct closure owner.

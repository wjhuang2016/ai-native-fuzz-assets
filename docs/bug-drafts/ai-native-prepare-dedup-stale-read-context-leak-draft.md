# COM_STMT_PREPARE dedup can keep stale-read semantics after the session variable is cleared

Status: confirmed on current source and testbed `8220955`; remote asset ID `id2010003`.

## Summary

When `tidb_enable_cache_prepare_stmt=ON`, preparing the same SQL again can reuse an old
`SnapshotTSEvaluator`. If the cache entry was created while `tidb_read_staleness=-1`, clearing
`tidb_read_staleness`, updating a row, and preparing the same SQL again still makes the new
prepared statement read the historical row version.

This affects MySQL binary-protocol prepare (`COM_STMT_PREPARE`). SQL text `PREPARE ... FROM ...`
is not an equivalent reproduction path.

## User-visible impact

The client explicitly disables stale reads and successfully updates a row from `1` to `2`. A new
prepared statement then silently returns `1`. The same SQL prepared with only the dedup fast path
disabled returns `2`.

The prepare-dedup feature is opt-in and default-off, which constrains reachability. The consequence
is still a direct wrong result: the documented stale-read contract says clearing
`tidb_read_staleness` returns subsequent reads to the latest data.

## Root cause

`variable.PrepareDedupCacheKey` binds SQL text, charset, collation, current database, SQL mode, and
schema version. It does not bind `ReadStaleness` or the derived snapshot evaluator.

On a cache hit, `session.rebuildFromPrepareCache` reparses the SQL and runs fresh Preprocess, but
then discards `ret.SnapshotTSEvaluator` and copies `cached.Stmt.SnapshotTSEvaluator`. The old
evaluator captured the prior `tidb_read_staleness=-1` duration, so execution of the newly prepared
statement still chooses a historical timestamp.

## Minimal protocol schedule

Use one physical MySQL connection:

```sql
SET @@tidb_enable_cache_prepare_stmt = 1;
CREATE TABLE t (id INT PRIMARY KEY, v INT);
INSERT INTO t VALUES (1, 1);
-- Wait until the initial version is older than one second.
SET @@tidb_read_staleness = -1;
-- COM_STMT_PREPARE + execute: SELECT v FROM t WHERE id = 1
SET @@tidb_read_staleness = '';
UPDATE t SET v = 2 WHERE id = 1;
-- COM_STMT_PREPARE + execute the identical SELECT again.
```

Observed on testbed `8220955`:

```text
dedup_on_after_clear  = 1
dedup_off_after_clear = 2
wrong_result_red      = true
```

Reusable client: `scaffolds/go-probes/prepare_dedup_staleness_repro.go`.

## Counterfactual

Changing only this field in `rebuildFromPrepareCache` makes the matrix pass:

```go
SnapshotTSEvaluator: ret.SnapshotTSEvaluator,
```

This proves that the stale evaluator, rather than plan cache, MVCC timing, or the update path,
owns the wrong result.

## Fix direction

Use the evaluator produced by the fresh Preprocess on every dedup hit. More generally, every
derived execution-context field copied from the template must either be bound by the dedup key or
rebuilt from the current session context. Audit `ForUpdateRead`, cacheability decisions, and other
copied derived fields under the same rule, without counting sibling variants as new roots.

## Evidence

- Local RED plus same-SQL dedup-off GREEN: `assets/store/logs/0091-prepare-dedup-read-staleness-red-control.log`
- One-field counterfactual GREEN: `assets/store/logs/0092-prepare-dedup-read-staleness-counterfactual-green.log`
- Real COM_STMT_PREPARE RED: `assets/store/logs/0093-prepare-dedup-read-staleness-testbed-red.log`
- Post-RED only dedup: local asset search and three upstream issue searches found no exact root.


# Pessimistic retry can advance seeded RAND and change failure into success

Status: confirmed on current source and real TiKV; filed as
[TiDB #69823](https://github.com/pingcap/tidb/issues/69823); remote `found_bug id2400003`;
high consequence, low trigger frequency.

## Summary

A pessimistic READ COMMITTED `UPDATE` can evaluate constant-seed `RAND(12345)`, hit a retryable
unique-key conflict, and transparently rebuild the statement. The failed attempt consumes the first
value from the statement's mutable `MysqlRng`. The retry consumes the second value and can therefore
choose a different key, report success, and commit a row that one execution from the same database
state would reject with duplicate key.

This is not ordinary nondeterminism. A constant initializer seed defines a deterministic sequence,
and TiDB creates the generator once before execution. The hidden attempt changes which deterministic
sequence position becomes user-visible.

## P/Q/F

- **P**: rebuilding the executor after `StmtRollback` and `ResetForRetry` makes the retry
  observationally equivalent to one statement execution.
- **Q**: the rebuilt expression starts from the statement-entry state of every mutable evaluator.
- **F**: `builtinRandSig.evalReal` advances `mysqlRng`, while `Clone` aliases the same pointer. The
  retry rebuild therefore consumes the already-advanced generator and publishes a later value.

## Minimal matrix

The first two values of `RAND(12345)` straddle `0.8`. Use that boundary to turn sequence drift into
opposing terminal outcomes:

```sql
CREATE TABLE retry_key(id INT PRIMARY KEY, u INT UNIQUE);
CREATE TABLE control_key LIKE retry_key;
INSERT INTO retry_key VALUES (1, 10);
INSERT INTO control_key VALUES (1, 10);
```

Session A:

```sql
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;
UPDATE retry_key
SET u = IF(RAND(12345) < 0.8, 1, 2) + SLEEP(20) * 0
WHERE id = 1;
```

While A is sleeping, session B commits:

```sql
BEGIN;
INSERT INTO retry_key VALUES (2, 1);
INSERT INTO control_key VALUES (2, 1);
COMMIT;
```

After A returns, commit it and run the direct control:

```sql
COMMIT;
SELECT * FROM retry_key ORDER BY id;

UPDATE control_key
SET u = IF(RAND(12345) < 0.8, 1, 2)
WHERE id = 1;
SELECT * FROM control_key ORDER BY id;
```

## Result

```text
hidden retry:  success, Exec_retry_count=1, rows=(1,2),(2,1)
direct control: ERROR 1062 Duplicate entry '1', rows=(1,10),(2,1)

single execution numeric value: 665703432
hidden retry committed value:    912825259
```

On real TiKV, the retry arm recorded `Exec_retry_count=1`, `Exec_retry_time=20.001772209`,
`Query_time=40.00417287`, `Succ=1`, and `IsExplicitTxn=1`. Metadata locking remained enabled.

## Source ownership

- `pkg/expression/builtin_math.go`: a constant seed creates one mutable `MysqlRng` before execution.
- `builtinRandSig.evalReal`: `Gen()` mutates that owner.
- `builtinRandSig.Clone`: the clone shallow-copies the `mysqlRng` pointer.
- `pkg/executor/adapter.go`: pessimistic retry rebuilds the executor from expression state already
  consumed by the failed attempt.

## Counterfactual and timing lesson

Declining transparent retry after a mutable `RAND` generator has been consumed makes the exact
unique-key test return the original conflict and preserve both rowsets. Merely deep-copying the RNG
inside `Clone` remains RED: by clone time the failed attempt has already advanced the source object.
The repair must restore a statement-entry snapshot or expose the original error.

## Quality

The symptom is silent persistent wrong data and a failure-to-success inversion, so the consequence
is C3/high. The trigger is narrow because a constant-seed `RAND` expression must run before a
retryable pessimistic conflict. Do not call it critical and do not enumerate seeds, thresholds,
random functions, DML forms, sleep values, or conflict shapes under this root.


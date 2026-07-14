# Method Case: RC write fast path versus FK snapshot ownership

## Finding

The source classified an ordinary `INSERT` as a TSO-reusable write, but a nested foreign-key
validator read through a different snapshot owner. A four-cell semantic matrix exposed a silent
lost write through `INSERT IGNORE`.

## P/Q/D/F/O/R/S card

```text
Target:
  Pessimistic READ-COMMITTED INSERT fast path with foreign-key validation.

Source anchors:
  sessiontxn/isolation/readcommitted.go:273-295  INSERT is accepted by the fast-path classifier.
  sessiontxn/isolation/readcommitted.go:322-345  statement snapshot gets RCCheckTS.
  sessiontxn/isolation/base.go:451-469             may return a separate snapshot object.
  executor/foreign_key.go:557-625                 FK helper reads txn.GetSnapshot directly.
  executor/foreign_key.go:636-650                 INSERT IGNORE enters the same checker.
  executor/write.go:248-258                       failed FK row is silently skipped.

P_check:
  A read-committed write fast path reuses an older TSO while the statement snapshot is marked
  RCCheckTS.

Q_claim:
  Every semantic read needed to validate the write, including FK parent existence, must observe
  the current read-committed rowset or force a retry before deciding.

D_dims:
  - parent committed before versus after child transaction BEGIN;
  - plain INSERT versus INSERT IGNORE;
  - tidb_rc_write_check_ts ON versus OFF.

F_effect:
  The executor-level snapshot and transaction-level snapshot can diverge. FK validation consumes
  the latter, sees the old rowset, and reports parent missing. INSERT IGNORE converts that report to
  an ignored row and warning.

O_oracle:
  In the after-BEGIN parent-commit cell, current parent exists and child must be inserted with one
  affected row. A successful statement with zero child rows is RED.

R_redflag:
  `Query OK, 0 rows affected, Warning 1452` plus an absent child row.

S_selector:
  `STATE_OWNER_SPLIT_IN_WRITE_FASTPATH`: after code proves P for the outer statement, enumerate
  semantic reads owned by helpers that do not receive the statement snapshot explicitly.
```

## Minimal matrix

| Parent timing | Write shape | Expected | Observed |
| --- | --- | --- | --- |
| before `BEGIN` | `INSERT IGNORE` | insert child | GREEN |
| after `BEGIN` | plain `INSERT` | insert child or current-state FK error only after current check | false `ERROR 1452` |
| after `BEGIN` | `INSERT IGNORE` | insert child, one affected row | RED: zero rows, warning 1452 |
| after `BEGIN`, `tidb_rc_write_check_ts=ON/OFF` | `INSERT IGNORE` | same current-state result | RED in both settings |

## Why this worked

The fast-path comment named the outer plan shape, not the complete semantic read set. The useful AI
step was to follow the proof obligation into the FK helper and ask who owns the snapshot there. The
small matrix then separated a false rejection from a silent lost write. `INSERT IGNORE` was the
severity amplifier: it turned the same stale read into a successful-looking statement.

## Sibling closure: unique-index reads in `UPDATE IGNORE`

A later current-source pass reused the selector without enumerating FK shapes. A pessimistic RC
point `UPDATE IGNORE` changed a UNIQUE value after another transaction deleted its old owner. The
outer PointGet row was unchanged, so the fast path's normal target-row conflict check was green, but
the in-place duplicate checker read the released unique key through the transaction snapshot. It
returned stale `ErrKeyExists`; `IGNORE` skipped the row, leaving no mutation that TiKV could use to
force a retry.

This improves the selector from "look for a helper that calls `txn.GetSnapshot`" to an execution
resource closure:

```text
outer plan proof
  -> enumerate every correctness read performed after the plan read
  -> map each row/index/FK/cascade/trigger resource to its snapshot owner
  -> map conflict/error to retry, fail, ignore, or partial continuation
  -> mutate one hidden resource while keeping the outer access key unchanged
```

The production schedule is ordinary account/order identifier release: writer A reads existing rows,
cleanup B deletes the current unique owner and commits, then A uses `UPDATE IGNORE` to claim the now
free identifier. The exact inequality is `A.latestOracleTS < B.commitTS < A.UPDATE`; all nodes are
healthy and MDL is ON. Current source returns success with zero affected rows and persists the old
value. A fresh TSO or excluding `Update.IgnoreError` from the fast path makes the same schedule
GREEN.

The sibling requires `tidb_rc_write_check_ts=ON`, whose current default is OFF. It is therefore a
validated high-consequence blast-radius surface, not another root or a default-config critical bug.
The OFF setting and release-before-BEGIN schedule are both GREEN controls.

## Pause gate

Do not enumerate foreign-key types, unique-index shapes, or IGNORE syntaxes. Reopen only when the
resource-closure table finds a different snapshot owner or retry policy, an upstream fix needs blast-
radius validation, or a sibling reaches a distinct irreversible consequence.

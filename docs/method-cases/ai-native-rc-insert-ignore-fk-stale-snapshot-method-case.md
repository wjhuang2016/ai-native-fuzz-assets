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

## Pause gate

Do not enumerate all foreign-key types, actions, or insert syntaxes. Reopen only for a distinct
snapshot-owner split, an upstream fix validation, or a sibling that proves a different severe
consequence.


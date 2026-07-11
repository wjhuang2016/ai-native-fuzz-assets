# Method Case: id1200002 RELEASE SAVEPOINT Stack Semantics

## Finding

TiDB's `RELEASE SAVEPOINT` deletes later savepoints. A later `ROLLBACK TO SAVEPOINT` then fails
with `ERROR 1305`, even though reference savepoint semantics keep later savepoints alive after
releasing an earlier one.

## P/Q/D/F/O/R/S Card

```text
Target:
  Transaction savepoint stack in session transaction context.

Source anchors:
  session.go:529-535 ReleaseSavepoint truncates the savepoint slice before the named marker.
  session.go:541-548 RollbackToSavepoint restores state and truncates later markers.
  simple.go:680-685 executeReleaseSavepoint directly trusts ReleaseSavepoint.

T_tests:
  Existing tests cover SAVEPOINT / RELEASE / ROLLBACK TO, but they assert the TiDB behavior where
  RELEASE s1/s2 deletes later savepoints. They do not compare against MySQL-compatible stack
  semantics.

P_check:
  The code finds the named savepoint in the ordered savepoint slice.

Q_claim:
  Releasing that savepoint leaves the transaction in a state consistent with SQL savepoint
  semantics.

D_dims:
  Operation-specific stack effect:
  - ADD/SAVEPOINT replaces duplicate name then appends a marker.
  - RELEASE removes only the named marker.
  - ROLLBACK TO restores state and discards later markers.

F_effect:
  RELEASE reuses rollback-like truncation, so later savepoints disappear. The failure is exposed
  only when a later marker is consumed.

O_oracle:
  Reference contract: MySQL-compatible savepoint semantics.
  Fast path: RELEASE sp1 then ROLLBACK TO sp2.
  Controls: ROLLBACK TO sp1 should delete sp2; RELEASE sp2 should preserve sp1.
  Required equality: marker reachability and in-transaction rowset match the reference contract.

R_redflag:
  Two ordered savepoints with a visible write after the second marker.

S_selector:
  S21 txn stack operation semantic split.
```

## Minimal Matrix

| Case | Sequence | Expected | TiDB |
| --- | --- | --- | --- |
| red | `sp1, sp2, RELEASE sp1, ROLLBACK TO sp2` | rollback succeeds | `ERROR 1305` |
| green | `sp1, sp2, ROLLBACK TO sp1, ROLLBACK TO sp2` | `sp2` gone | `ERROR 1305` |
| green | `sp1, sp2, RELEASE sp2, ROLLBACK TO sp1` | `sp1` usable | rollback succeeds |

## Why This Worked

The code was not suspicious because of a complex branch. It was suspicious because two neighboring
operations over the same stack had different contracts but one implementation used the other's
mutation shape. The two-marker matrix is enough to distinguish "delete named marker" from
"truncate later markers"; no random transaction fuzzing is needed.

## Next Use

For txn, search for more state containers where adjacent operations share helpers but have
different contracts: lock wait state, statement history, transaction flags, schema-change retry
state, or stale-read transaction context. Only pursue a target if a user-visible consumer and a
reference/safe-path oracle are available before writing cases.

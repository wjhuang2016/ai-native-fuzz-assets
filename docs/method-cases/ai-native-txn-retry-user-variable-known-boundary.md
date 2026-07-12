# Optimistic Retry And Read-Only Session State: Known Boundary

## Question

Can an optimistic transaction retry become a severe wrong-result bug when a
read-only statement changes session state used by a later write?

## Proof Obligation

- `P`: optimistic commit retry rebuilds a new transaction and replays the saved
  statement history.
- `Q`: `finishStmt` records retry-safe writes in `StmtHistory`; a read-only
  `SELECT @v := ...` is not a write statement and is not recorded.
- `F`: if the source row changes between the first execution and the retry,
  the later write can use the old session variable value.

## Minimal Probe

```sql
BEGIN;
SELECT @retry_value := v FROM retry_src WHERE id = 1; -- 10
UPDATE retry_dst SET v = @retry_value WHERE id = 1;
COMMIT; -- force one optimistic commit retry
```

The retry hook changes `retry_src.v` from `10` to `20` before the new retry
transaction is prepared. The observed final `retry_dst.v` remains `10`, and
the retry log contains the write history but not the `SELECT` assignment.

Evidence: `assets/store/logs/txn-retry-user-variable-known-boundary-20260712.log`
and `assets/store/txn-retry-user-variable-known-boundary-results.jsonl`.

## Contract Gate

This is not a new bug root. TiDB's optimistic transaction documentation states
that automatic retry re-executes only SQL statements that contain write
operations, and warns that using other query results during retry can violate
Repeatable Read consistency. The same documentation says automatic optimistic
transaction retry is deprecated from v8.0 and recommends handling conflicts in
the application or using pessimistic transactions:

<https://docs.pingcap.com/tidb/stable/optimistic-transaction/>

Therefore the old value is a documented consequence of an opt-in/deprecated
semantic, not evidence that the current default transaction path silently
violates its product contract.

## Method Lesson

`wrong-result` is not sufficient for promotion. The contract gate must be run
after the strong oracle:

1. Does the product promise full statement replay or only write replay?
2. Is the mode enabled by default, supported in the current release, and
   recommended for production?
3. Does the candidate cross the documented limitation, or merely demonstrate
   the limitation?

This case is retained as a negative calibration for the retry/state-replay
selector and is excluded from the bug count.

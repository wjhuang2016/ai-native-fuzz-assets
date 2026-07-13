# id2310003: retry closure includes external capability ownership

## Why the existing LOOP found it

This pass reused `ATTEMPT_SCOPED_SIDE_EFFECT_ROLLBACK_CLOSURE` after two earlier state-owner hits.
It did not reopen UserVars or `LAST_INSERT_ID`. Instead, it enumerated side effects whose highest
consumer lives outside statement KV and `StatementContext`. Advisory locks ranked first because a
failed-attempt mutation creates an independently observable exclusion capability.

```text
failed attempt -> session advisory-lock map -> internal pessimistic lock transaction
       |                    |
       +-> KV rollback      +-> no retry rollback owner
successful zero-work attempt -> success response -> competitor remains blocked
```

## Minimal matrix

1. Natural local conflict: first attempt evaluates row-dependent `GET_LOCK` and sleeps; another
   transaction claims the unique key and inserts a gate; retry count is one and success matches zero
   rows; competitor gets `0`.
2. SQL-only real TiKV: the same schedule records retry count one in the slow log; `IS_USED_LOCK`
   equals the successful statement's connection ID and competitor gets `0`.
3. Same-final-state control: start with the key and gate already committed; zero-row DML never
   evaluates the row-dependent function; `IS_USED_LOCK` is `NULL` and competitor gets `1`.
4. Cleanup witness: releasing from the hidden owner changes the competing result from `0` to `1`.

The function argument depends on the current row. This prevents planning or constant folding from
executing `GET_LOCK` in the zero-row control and makes the matrix about retry ownership only.
The reusable probe is
`scaffolds/tidb-tests/manual/ai_native_pessimistic_retry_advisory_lock_test.go`.

## Method improvement

S45 originally had two consumers: state read during re-entry and state published at statement
completion. This case adds a third:

1. re-entry consumer;
2. terminal publication consumer;
3. **external capability consumer**: a failed attempt changes a lock, lease, registration, handle,
   or other independently live owner that remains observable after the successful attempt.

For the third class, a scalar return value is not a strong oracle. Query the external owner and then
exercise a competing operation. Require both ownership and denial, followed by cleanup recovery.
The same-final-state control remains mandatory.

## Severity and stop rule

This is high consequence but low frequency: `GET_LOCK` must be evaluated inside pessimistic DML and
a later retryable conflict must occur. Long-lived pooled sessions can turn the residual lock into an
unbounded job or service stall. SQL variants, lock names, retry counts, and alternative conflict
windows are blast radius. Reopen this selector only for a different external capability owner or a
different retry boundary. Status: issue-filed as remote `id2310003` and upstream
[TiDB #69820](https://github.com/pingcap/tidb/issues/69820).

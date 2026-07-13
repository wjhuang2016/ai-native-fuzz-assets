# Pessimistic retry can retain an advisory lock from a rolled-back attempt

Status: issue-filed on current source and real TiKV with metadata locking enabled. Remote bug ID:
`id2310003`; upstream [TiDB #69820](https://github.com/pingcap/tidb/issues/69820).

## User-visible failure

A pessimistic READ COMMITTED `UPDATE` evaluates `GET_LOCK()` and then encounters a retryable unique
key conflict. The rebuilt executor sees newly committed state, matches zero rows, and returns
success. Although the successful attempt never evaluates `GET_LOCK()`, the session still owns the
lock acquired by the failed attempt. Another session is denied until the hidden owner explicitly
releases the lock or disconnects.

This can stall a distributed scheduler or singleton job indefinitely when pooled connections are
long lived. The trigger is low frequency because it requires an advisory-lock function inside
retryable DML, but the consequence is high for workloads that use the lock as a correctness or
liveness boundary.

## Proof obligation

```text
P: StmtRollback plus ResetForRetry return a failed statement attempt to retry-entry state
Q: external capabilities owned only by the failed attempt cannot survive a successful retry
F: GET_LOCK mutates session.advisoryLocks and an independent internal transaction;
   the retry owner rolls back statement KV state but never restores that capability owner
consumer: IS_USED_LOCK and a competing GET_LOCK observe the hidden owner after zero-row success
```

## Strong oracle

| Cell | Retry count | Successful rows | IS_USED_LOCK | Competing GET_LOCK |
| --- | ---: | ---: | --- | ---: |
| Natural conflict, failed attempt evaluates GET_LOCK | 1 | 0 | owner connection | 0 |
| Same final database state, no failed attempt | 0 | 0 | NULL | 1 |

The local natural-conflict probe observed `Exec_retry_count=1`. On testbed `8220955`, the slow log
reported `exec_retry_count=1`, `succ=1`, and the SQL-only schedule produced the same RED. In both
environments metadata locking remained enabled; the live run recorded
`@@global.tidb_enable_metadata_lock=1`.

## Root cause

`builtinLockSig.evalInt` calls `GetAdvisoryLock`. A new lock creates a dedicated internal session
and pessimistic transaction and stores an `advisoryLock` in `session.advisoryLocks`; repeated
acquisition increments its reference count. After the later write conflict,
`handlePessimisticLockError` calls transaction retry hooks, rebuilds the executor, rolls back
statement KV changes, and resets `StatementContext`. None of those owners restore the advisory-lock
map or close the internal lock transaction.

The rebuilt RC executor observes the gate row and does no work, so the leaked capability is not
overwritten or surfaced in the successful attempt's result.

## Fix direction

The conservative fix is to classify statements containing advisory-lock operations as unsafe for
transparent pessimistic retry before execution and surface the original conflict. A complete retry
journal would need to cover `GET_LOCK`, repeated acquisition, `RELEASE_LOCK`, and
`RELEASE_ALL_LOCKS`; restoring a released external lock can block or fail, so partial cleanup is not
sufficient.

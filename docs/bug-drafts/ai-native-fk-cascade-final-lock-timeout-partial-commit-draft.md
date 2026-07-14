# Failed FK cascade UPDATE can be committed after lock wait timeout

Status: issue-filed high severity / critical consequence on current TiDB with mock and real TiKV.
Upstream: [TiDB #69838](https://github.com/pingcap/tidb/issues/69838).

## Impact

A pessimistic multi-table UPDATE returns definite error 1205, but a later COMMIT of the still-open
explicit transaction makes the failed statement's parent-key update and generated `ON UPDATE CASCADE`
child update durable. The service can retry an operation it was told had failed while the first attempt
has already changed relational data.

This is a statement-atomicity violation. The consequence is severe; production reachability is narrower
because the statement needs a parent-key cascade, an unchanged row that is locked in the final phase,
and an application that commits the explicit transaction after handling 1205.

## Concrete production trigger

A tenant/account identifier migration updates a parent primary key and cascades the new identifier into
dependent routing or settlement rows. The same multi-table UPDATE uses
`migration_guard.version = migration_guard.version` as a database mutex. An older batch worker already
owns that guard row and retains it longer than the default 50-second lock timeout because it is processing
a large batch, backing off on a hot/server-busy TiKV Region, or slowed by storage pressure.

The racing migration executes the parent update and cascade before waiting on the guard in final
`LockKeys`. It receives 1205. Since lock timeout rejects the statement but leaves the explicit transaction
open, a service that catches the retryable conflict and commits earlier audit/progress work issues COMMIT.
That COMMIT unexpectedly publishes the failed migration too. Services that always ROLLBACK on 1205 are
not exposed, but no client behavior can make persistence of a failed statement valid.

```text
B guard lock
  < A parent mutation
  < A cascade mutation
  < intermediate StmtCommit
  < FK savepoint release
  < final guard LockKeys timeout
  < client sees 1205
  < client COMMIT
```

The real-TiKV RED used default MDL ON, default 50-second lock timeout, default pessimistic in-place
constraint checking, and current default FK enablement. One TiDB, one TiKV, and two sessions suffice;
there is no failpoint, DDL race, Region split, async commit, or 1PC.

## Durable oracle

Initial and expected post-COMMIT state after the failed UPDATE:

```text
parent.id=1, child.pid=1
```

Observed fresh-session state:

```text
parent.id=2, child.pid=2
```

The default-settings run recorded `LockKeys_time=50.001702208`, returned
`[tikv:1205] Lock wait timeout`, and failed the expected `[1 1]` assertion with actual `[2 2]`.

## Root cause

`prepareFKCascadeContext` records a transaction savepoint because FK execution uses intermediate
`StmtCommit` calls to expose parent/cascade mutations to nested executors. The savepoint is released as
soon as `handleStmtForeignKeyTrigger` succeeds, even though the outer pessimistic statement still has a
fallible final `LockKeys` phase. A terminal lock error then follows generic `StmtRollback`, which cannot
undo mutations already published across stages after the checkpoint owner was released.

Root ID: `fk-cascade-savepoint-released-before-final-lock-result`.

## Exact counterfactual

Retain the FK savepoint through final pessimistic locking. On a terminal post-trigger lock error, roll
the transaction mem-buffer back to that checkpoint; release it only when the complete user statement
succeeds. The same 1205 schedule becomes GREEN and leaves `[1 1]` on mock and real TiKV.

This is independent of id2490003 / #69828. That root loses lock ownership and permits two successful
transactions to leave an orphan. This root loses rollback ownership and persists a statement that
definitely failed.

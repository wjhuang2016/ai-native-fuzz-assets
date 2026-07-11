# id30008: cross-schema `RENAME TABLE` can leave stale table lock after `UNLOCK TABLES`

## Status

Confirmed DDL/table-lock correctness bug found by the proof-obligation small matrix.

Current evidence:

```text
go test -tags=intest ./pkg/ddl -run TestAITableLockCrossDBRenameUnlockProbe -count=1
=> FAIL: post-unlock INSERT sees [schema:8020] Table 't' was locked in WRITE by the original session.

go test -tags=intest ./pkg/ddl -run TestRenameTableWithLocked -count=1
=> PASS: same-schema locked-table rename remains the green control.

testbed 8192975 after enabling table lock:
session1 LOCK/RENAME/UNLOCK => rc=0
session2 INSERT INTO ai_lock_dst_live.t VALUES (1)
=> ERROR 8020: Table 't' was locked in WRITE by server: ... session: ...
```

`enable-table-lock` is disabled by default. The user-provided testbed `8192975` initially reported `enable-table-lock=false`, so it was a capability skip until the TiDB config was changed. After updating `tc.spec.tidb.config` and restarting `fp-tidb` with `enable-table-lock=true`, the same cluster reproduced the bug. The bug is DDL-owned: the stale state is created by `LOCK TABLES`, moved by `RENAME TABLE`, and should be removed by `UNLOCK TABLES`.

## Minimal Repro

Run on a TiDB with `enable-table-lock = true`:

```sql
CREATE DATABASE ai_lock_src;
CREATE DATABASE ai_lock_dst;
CREATE TABLE ai_lock_src.t (a INT);

-- session 1
LOCK TABLES ai_lock_src.t WRITE;
RENAME TABLE ai_lock_src.t TO ai_lock_dst.t;
UNLOCK TABLES;

-- session 2
INSERT INTO ai_lock_dst.t VALUES (1);
```

Observed:

```text
ERROR 8020 (HY000): Table 't' was locked in WRITE by server: ..._session: 1
```

## Expected

After `UNLOCK TABLES` succeeds, `ai_lock_dst.t` should be unlocked. Other sessions should be able to insert/select normally.

## Actual

`UNLOCK TABLES` returns success in session 1, but the table lock remains attached to `ai_lock_dst.t`. A second session cannot write the renamed table and receives `ErrTableLocked`.

## Source Chain

- `/Users/bba/pc/tidb/pkg/session/session.go:271` stores session table locks by `TableID`, but the stored value also contains the original `SchemaID`.
- `/Users/bba/pc/tidb/pkg/ddl/table.go:901` implements rename by dropping the table from `OldSchemaID` and creating the same `TableInfo` under `NewSchemaID`.
- `/Users/bba/pc/tidb/pkg/ddl/table.go:924` explicitly notes that cross-schema rename keeps `TableID` while schema ID can change, but only updates `AutoIDSchemaID`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5819` builds the `UNLOCK TABLES` job from `ctx.GetAllTableLocks()`, so it uses the stale `SchemaID` recorded before rename.
- `/Users/bba/pc/tidb/pkg/ddl/table_lock.go:170` unlocks by `(SchemaID, TableID)`. If the table is no longer in the old schema, it treats `ErrTableNotExists` as "maybe dropped" and skips cleanup.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5859` then clears the session-local lock map when the unlock job succeeds, leaving no ordinary session handle to release the remaining `TableInfo.Lock` on the new-schema table.

Root model:

```text
LOCK TABLES stores owner key = old SchemaID + stable TableID
cross-schema RENAME TABLE changes SchemaID but preserves TableID
UNLOCK TABLES trusts the stale owner key
=> unlock skips the live table and leaves TableInfo.Lock behind
```

## Fix Direction

Two reasonable repair semantics:

1. Update the session table-lock entry when a locked table is renamed across schemas, so later `UNLOCK TABLES` uses the new `SchemaID`.
2. Make unlock resolve the current schema by `TableID` when `(SchemaID, TableID)` misses, instead of assuming the table was dropped.

Blocking cross-schema rename of a locked table is also possible, but it would be a behavior change from the existing same-schema locked-table rename support.

## Method Takeaway

This hit came from a new selector:

```text
DDL-created session/cache side state
+ object move/rekey path preserves ID but changes owner/container key
+ cleanup path trusts the old owner/container key
+ post-DDL behavior oracle exists
= high-value small matrix
```

The small matrix was intentionally narrow:

| Cell | Result | Meaning |
|---|---|---|
| same-schema locked-table rename + unlock | green | table-lock rename support exists and is not globally broken |
| cross-schema locked-table rename + unlock | red | schema/container key is not rewritten for the session lock entry |

Do not expand table-lock variants yet. The useful next work is to agree on fix semantics for cross-schema rename/unlock, then validate only the owner-key rewrite contract.

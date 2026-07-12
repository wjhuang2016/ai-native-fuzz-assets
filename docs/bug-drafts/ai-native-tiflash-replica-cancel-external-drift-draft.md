# id1830003: cancelled TiFlash replica removal leaves stale available metadata

## Status

- Severity: high
- Status: issue-filed: https://github.com/pingcap/tidb/issues/69785
- Root cause ID: `tiflash-rule-delete-before-ddl-commit`
- Affected path: nonpartition `ALTER TABLE ... SET TIFLASH REPLICA 0`

## User-visible symptom

An operator cancels `SET TIFLASH REPLICA 0` and receives `ERROR 8214 Cancelled DDL job`. DDL
history is cancelled and `INFORMATION_SCHEMA.TIFLASH_REPLICA` still says `REPLICA_COUNT=1,
AVAILABLE=1`. However PD has already removed the table's TiFlash rule.

For a session configured with `tidb_isolation_read_engines='tiflash'`, TiDB still builds an
`mpp[tiflash]` plan from that committed metadata, then returns `ERROR 9012 TiFlash server timeout`.

## Proof obligation

`onSetTableFlashReplica` calls `ConfigureTiFlashPDForTable` before updating `TiFlashReplicaInfo`
and finishing the DDL job. For count 0, the external call deletes `tiflash/table-<id>-r`. A later
supported cancellation rolls back local metadata but has no compensation edge for PD.

```text
P: metadata count=1/available=true, PD rule count=1, TiFlash query succeeds
Q: cancelled count=0 DDL preserves that committed state in every owner
F: PD rule deletion commits before the still-abortable metadata transaction
```

## Real reproduction

On testbed 8220955, a compatible TiFlash store was added temporarily. Table 5378 contained five
rows. Before the fault, metadata progress was 1, PD rule count was 1, EXPLAIN used `mpp[tiflash]`,
and the TiFlash-only aggregate returned `5,150`.

The worker paused after PD deleted the rule. `ADMIN CANCEL DDL JOBS 5382` succeeded; ALTER returned
8214 and history became cancelled. Metadata remained count 1/available 1, the PD rule remained
absent, and the same TiFlash-only query returned 9012 timeout.

## Controls

- Restoring only the captured PD rule changed progress to 1 and restored query result `5,150`.
- A normal replica-removal job 5383 synced successfully and removed both metadata and PD state.
  The TiFlash-only session then received immediate 1815 `No access path`, which is the expected,
  explicit behavior for a declared removal.
- The mock-TiFlash matrix reproduced the owner split and passed normal/compensation controls.

## Root cause and fix direction

The DDL metadata transaction, PD rule store, TiFlash scheduler, and query consumer are separate
durable owners. A robust fix should commit a durable reconciliation intent or compensate every
post-PD abort from committed metadata. Reordering one RPC can invert the failure window; the real
invariant is eventual owner convergence before a terminal DDL result is exposed.

## Discovery provenance

The candidate came from a current-source external-owner scan using S35. Local mock RED and the
real PD/TiKV/TiFlash query RED were completed before upstream search. No PR review finding was used.

# Draft: FLASHBACK DATABASE restores dangling placement policy ref (id30011)
> 2026-07-02. Status: confirmed on testbed 8192975 (master `5c9198e948`). Pause gate active — do not expand restore-path variants before fix-direction discussion.

## Minimal reproduction (6 statements, deterministic)

```sql
CREATE PLACEMENT POLICY p FOLLOWERS=1;
CREATE DATABASE d PLACEMENT POLICY=p;
DROP DATABASE d;
DROP PLACEMENT POLICY p;      -- succeeds: in-use scan only sees live schemas
FLASHBACK DATABASE d;         -- succeeds: DBInfo restored verbatim
SHOW CREATE DATABASE d;
-- CREATE DATABASE `d` ... /*T![placement] PLACEMENT POLICY=`p` */   <- p no longer exists
CREATE TABLE d.t(a INT);
-- ERROR 8239 (HY000): Unknown placement policy 'p'
```

User impact: a successfully recovered database rejects **every** `CREATE TABLE` until the user
manually runs `ALTER DATABASE d PLACEMENT POLICY=DEFAULT`. `SHOW CREATE DATABASE` output is not
round-trippable. Recreating a same-name policy "heals" table creation (name-bound resolution),
which can silently attach the wrong (new) policy semantics.

## Source chain

- `pkg/ddl/schema.go` `onRecoverSchema`: `dbInfo := schemaInfo.Clone(); dbInfo.State = StatePublic; metaMut.CreateDatabase(dbInfo)` — the dropped DBInfo is restored **verbatim**; no placement-ref sanitization, no validation call.
- `pkg/ddl/table.go:291` `recoverTable` → `clearTablePlacementAndBundles` (table.go:322): the sibling **object** path deliberately nils table/partition `PlacementPolicyRef` and resets PD bundles — the stated design is "recovered objects drop placement".
- `checkPlacementPolicyRefValidAndCanNonValidJob` exists only on the `ALTER DATABASE` path (schema.go:134); the recover path never calls it.
- `DROP PLACEMENT POLICY`'s in-use scan covers live schemas only, so a dropped-but-recoverable DB does not protect its policy.

## Additional asymmetry (same root)

With the policy still alive: `FLASHBACK DATABASE` keeps the DB-level ref while the restored
tables inside lose their table-level refs (`db_keeps_ref=True table_keeps_ref=False`).
Restore semantics are inconsistent within a single statement.

## Fix direction

Consistent with the sibling path: `onRecoverSchema` should clear `dbInfo.PlacementPolicyRef`
the same way `clearTablePlacementAndBundles` does for tables — or validate the ref and reset
to nil with a warning when the policy is missing. Fix validation should cover: dropped policy,
alive policy (consistency), partitioned tables inside the flashbacked DB, and
`FLASHBACK DATABASE ... TO new_name`.

## Probe

`/Users/bba/pc/ai_native_ddl_flashback_placement_probe.py` — 6 cells, findings=2
(`flashback_db_ref_to_dropped_policy`, `dangling_consequence_create_table`),
greens carry trigger evidence. Audit card: `/Users/bba/pc/ai-native-flashback-placement-audit-card.md`.

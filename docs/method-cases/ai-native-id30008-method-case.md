# Method Case id30008: owner-key rewrite for DDL-created session state

## Result

When `enable-table-lock=true`, `LOCK TABLES src.t WRITE; RENAME TABLE src.t TO dst.t; UNLOCK TABLES;` can leave `dst.t` locked. A second session then gets `ErrTableLocked` on `INSERT INTO dst.t`.

Artifacts:

- Draft: `/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md`
- Bug DB: `found_bug id30008`, now `confirmed=1,status=confirmed` after testbed reproduction.

## Why This Worked

The starting proof obligation was:

```text
If DDL moves a locked table,
the table-lock owner state must still point at the live table,
and UNLOCK TABLES must remove the lock from the live table.
```

The code made this obligation precise:

```text
session lock map value contains SchemaID + TableID
cross-schema rename preserves TableID but changes SchemaID
unlock job uses the stored SchemaID + TableID
unlock worker ignores missing old-schema table as "maybe dropped"
```

That gives a one-bit matrix:

```text
same-schema rename: owner key unchanged => green control
cross-schema rename: owner key changes => red candidate
```

The oracle is not a metadata assertion. It is user-visible behavior:

```text
after UNLOCK TABLES succeeds,
another session must be able to INSERT/SELECT the renamed table
```

The red cell failed with `[schema:8020] Table 't' was locked in WRITE by ... session: 1`. It reproduced on the user-provided testbed `8192975` after updating `tc.spec.tidb.config` and restarting `fp-tidb` with `enable-table-lock=true`.

## Selector Upgrade

id30007 gave us a sibling-iterator selector:

```text
common owner paths green
+ sibling path has different all-objects iterator
+ rowset/ADMIN CHECK oracle
```

id30008 adds a different DDL-only selector:

```text
DDL-created side state includes both object ID and owner/container key
+ DDL move/rekey path preserves object ID but changes owner/container key
+ cleanup path trusts the old owner/container key
+ behavior oracle exists after cleanup
= high-value small matrix target
```

This is broader than table lock. It applies to session/cache/sys-table state where a table, database, partition, or schema object can move while the side state stores an old container key.

## Why It Is Not Drift

This stays inside the DDL lane:

- target DDL: `RENAME TABLE src.t TO dst.t`
- owner: table-lock metadata in `TableInfo.Lock` plus session table-lock map
- cleanup DDL/API: `UNLOCK TABLES`
- consequence oracle: post-DDL table access from another session

The executor is only used to observe whether the DDL cleanup obligation held.

## Pause Gate

This confirmed bug has reached the pause gate.

Checklist:

- Minimal repro: one locked table, one cross-schema `RENAME TABLE`, one `UNLOCK TABLES`, one second-session `INSERT`.
- Green neighbor: existing same-schema locked-table rename test passes.
- Oracle: post-unlock behavior from another session, not raw `TableInfo.Lock` inspection.
- Root model: `SchemaID` in the session lock entry is stale after cross-schema rename.
- Repair contract: either rewrite the session lock entry on cross-schema rename or make unlock resolve the current schema by `TableID`.
- Stop rule: do not expand to drop database, multi-table rename, close-session cleanup, or read/write lock variants until owner-key rewrite semantics are agreed.

## Next Improvement

For the next source scan, prefer owners with this shape:

```text
side state key = container ID/name + object ID
DDL path moves/rekeys the object without changing object ID
cleanup path keys by the old container
```

This asks the AI to reason about identity and ownership separately, which is exactly where shallow SQL fuzz is unlikely to focus.

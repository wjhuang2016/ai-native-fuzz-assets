# id1200001 Method Case: CREATE TABLE LIKE copies READ ONLY table lock

## Result

- Remote `found_bug`: id1200001, confirmed.
- Root cause: `create-like-copies-table-lock`.
- Counts after insert: `COUNT(*)=70`, `COUNT(DISTINCT root_cause_id)=48`.

## Method Shape

This is an S13 refinement:

```text
DDL reconstruction path
+ top-level source metadata copy
+ explicit reset list proves some fields are target-only
+ unreset field is runtime state, not schema definition
+ target behavior oracle exists
= target runtime-state clone bug
```

## Why This Was Fast

The search did not enumerate LIKE syntax. It inspected `BuildTableInfoWithLike` as a proof:

```text
Code checked P:
  shallow-copy source TableInfo and reset selected target-only fields.

System believed Q:
  the remaining copied fields are safe schema definition for a new table.

Fast path:
  publish the new TableInfo without rebuilding every field from schema-owned inputs.

Counterexample dimension:
  TableInfo.Lock is runtime state. It is not part of CREATE TABLE schema.
```

The red oracle was a two-object behavior differential:

```sql
ALTER TABLE src READ ONLY;
CREATE TABLE dst LIKE src;
INSERT INTO dst VALUES (2); -- ERROR 8020
ALTER TABLE dst READ WRITE;
INSERT INTO dst VALUES (3); -- succeeds
INSERT INTO src VALUES (3); -- still ERROR 8020
```

## Quality

Medium. A successful DDL creates an unexpectedly read-only table. This is user-visible and
actionable, but not silent data corruption.

## Novelty

This is not the earlier table-lock cross-schema rename root. The fix locus is different:

- id30008: session lock cleanup trusts stale `SchemaID` after a table move.
- id1200001: `CREATE TABLE LIKE` copies `TableInfo.Lock` into a new object.

It is also not the earlier CHECK-name LIKE source mutation. The reused selector is S13, but the
new D dimension is target runtime-state cloning rather than pointer-backed source metadata
mutation.

## Stop Rule

Do not enumerate every `TableInfo` field mechanically. Reopen S13 for this branch only when:

- another unreset runtime/non-schema field has a behavior oracle;
- the consequence escalates beyond an unexpected target state;
- or this bug's fix needs validation across locked and unlocked source tables.

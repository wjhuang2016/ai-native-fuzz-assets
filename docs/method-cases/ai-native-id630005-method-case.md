# id630005 Method Case: Shallow Copy Target Mutation

## Selector

```text
S13_DDL_SHALLOW_COPY_TARGET_MUTATION
```

This selector applies when a DDL path reconstructs a new object from an existing object, then
normalizes target-only metadata in place.

```text
P_check:  source TableInfo is copied to become the target TableInfo
Q_claim:  target-only rewrites cannot affect the source object
effect:   publish target metadata after mutating shared pointer fields
D_dim:    source and target share nested metadata objects after a shallow copy
```

## Matrix

| Cell | Initial state | Operation | Oracle | Result |
| --- | --- | --- | --- | --- |
| Direct sibling tables | `d1`, `d2` each created with anonymous CHECK | two direct `CREATE TABLE` statements | names stay `d1_chk_1`, `d2_chk_1` | GREEN |
| LIKE auto-named CHECK | `src_auto(a int, CHECK(a>0))` | `CREATE TABLE dst_auto LIKE src_auto` | source `SHOW CREATE` must stay `src_auto_chk_1` | RED, source becomes `dst_auto_chk_1` |
| New connection | same as above | reconnect and `SHOW CREATE src_auto` | mutation must not be session-local illusion | RED, still `dst_auto_chk_1` |
| Runtime error | same as above | `INSERT INTO src_auto VALUES (-1)` | source error should name source constraint | RED, error names `dst_auto_chk_1` |
| I_S cross-check | same as above | query `information_schema.check_constraints` | metadata surfaces should agree | RED, I_S still lists source and target names |

## Oracle

```text
O16_SOURCE_TARGET_METADATA_ISOLATION_ORACLE
```

The reference is simple: creating a new object from an existing object must not change any
SQL-visible metadata of the existing object. Direct sibling creates are the green control.

## Why The Method Worked

The source shape was a classic target-reconstruction trap:

```text
tblInfo := *referTblInfo
rename target CHECK constraints
publish target table
```

The top-level struct copy looked harmless, but `Constraints` is a slice of pointers. The target
renaming code therefore rewrote the shared `ConstraintInfo` objects. The AI-search step did not
need random SQL. It only had to ask:

```text
which nested fields are pointer-backed, and does the target-normalization code mutate them?
```

## Quality

Medium.

- User-visible symptom: `SHOW CREATE TABLE src` and CHECK violation errors show the target
  constraint name after `CREATE TABLE dst LIKE src`.
- Metadata surfaces disagree: `information_schema.check_constraints` still exposes both names.
- No row-level data corruption was observed; CHECK enforcement still blocks invalid rows.
- Method value is high because S13 is a compact DDL construction pattern that can generalize to
  other clone/rebuild paths.

## Pause Gate

Do not enumerate every `CREATE TABLE LIKE` option. Reopen S13 only for:

- another pointer-backed metadata owner mutated by target reconstruction;
- a source mutation that changes behavior, not only display metadata;
- fix validation that proves all nested TableInfo fields are cloned or intentionally shared.


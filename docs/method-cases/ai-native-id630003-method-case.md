# id630003 Method Case: Partition-Column Validation Metric Mismatch

## Selector

```text
S10_DDL_VALIDATION_METRIC_MISMATCH
```

This case uses the same proof-obligation shape as id630001/id630002, but on a different DDL owner:
partition-column modify validation.

```text
P_check:  partition-column MODIFY allowlist only allows string length extension
Q_claim:  any string length shrink of a partition column requires repartition and is unsafe
effect:   reject ALTER before target partition-definition validation and data-fit checking
D_dim:    target schema, partition literals, and existing rows can all fit the shorter VARCHAR
```

## Matrix

| Cell | Initial state | Target state | Oracle | Result |
| --- | --- | --- | --- | --- |
| Direct LIST target | none | `varchar(5)` LIST COLUMNS literals `abc`,`xyz` | create + insert succeeds | GREEN |
| Direct RANGE target | none | `varchar(5)` RANGE COLUMNS bound `'m'` | create + insert succeeds | GREEN |
| Direct KEY target | none | `varchar(5)` KEY(a) | create + insert succeeds | GREEN |
| Non-partition control | `varchar(6)` rows max char length 3 | `varchar(5)` | ALTER succeeds | GREEN |
| LIST shrink | `varchar(6)` LIST COLUMNS literals/data length 3 | `varchar(5)` | should match target/data-fit reference | RED, ERROR 8200 |
| RANGE shrink | `varchar(6)` RANGE COLUMNS bound/data fit | `varchar(5)` | should match target/data-fit reference | RED, ERROR 8200 |
| KEY shrink | `varchar(6)` KEY(a), sampled values fit | `varchar(5)` | direct old/new partition membership sampled equal | RED, ERROR 8200 |
| Widen control | `varchar(6)` LIST COLUMNS | `varchar(7)` | checker-aligned transition | GREEN |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

The oracle has three arms:

1. Direct target schema: can the final partitioned table be created and populated directly?
2. Non-partitioned transition: can TiDB's generic `MODIFY COLUMN` prove the same values fit?
3. Checker-aligned control: does the existing partition validator allow the corresponding widen?

The red cells are meaningful because all three guards line up: target schema is valid, data fits,
and the current validator's positive path works for widen.

## Why The Method Worked

The previous FK bug gave a stop rule: do not enumerate FK type pairs; look for a different DDL
validator whose transition check is stricter than the final-schema contract.

The partition-column code had exactly that asymmetry:

```text
target-state validator exists:
  buildPartitionDefinitionsInfo(..., newTblInfo, ...)

but before it runs:
  isStringLengthExtension requires newFlen > oldFlen
```

So the search did not need random DDL generation. It only needed a small matrix around the hidden
inequality:

```text
newFlen < oldFlen
partition literals fit newFlen
existing rows fit newFlen
direct target schema accepted
```

## Quality

Medium.

- User-visible symptom: valid-looking DDL fails with `ERROR 8200`.
- Strong oracle: direct target partition schema and non-partitioned transition both succeed.
- Source is localized to `/Users/bba/pc/tidb/pkg/ddl/modify_column.go`.
- It is not data loss or wrong result.
- It is methodologically useful because it proves S10 can jump from FK validation to partition-column validation.

## Pause Gate

Do not enumerate every partition type and string type. Reopen only for:

- a silent wrong-acceptance consequence in partition-column modify;
- a different metric, such as encoded key bytes or collation weight;
- fix validation across LIST/RANGE/KEY, binary/non-binary strings, and row/literal fit boundaries.

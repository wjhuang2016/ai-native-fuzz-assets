# id630001 Method Case: DDL Precheck Metric Mismatch

## Selector

```text
S10_DDL_PRECHECK_METRIC_MISMATCH
```

The code validates whether existing data can survive a metadata-only or no-reorg DDL by running a simplified precheck. If the precheck uses a cheaper or more convenient metric than the real type/semantic contract, it can reject valid data or allow invalid data.

This is a high-signal selector whenever:

```text
P_check:  a restricted SQL precheck finds no violating row
Q_claim:  all existing rows satisfy the target DDL contract
fast path: publish metadata / skip row rewrite or safe conversion
D_dim:    the precheck metric is not the same unit as the target contract
```

## Matrix

| Cell | Old type | Value | Target type | Oracle | Result |
| --- | --- | --- | --- | --- | --- |
| Target reference | none | `中中中` | `varchar(3)` | direct insert succeeds | GREEN |
| Target reference | none | `中中中` | `char(3)` | direct insert succeeds | GREEN |
| ASCII control | `varchar(4)` | `abc` | `varchar(3)` | ALTER succeeds | GREEN |
| Red varchar | `varchar(4)` | `中中中` | `varchar(3)` | direct target accepts value, ALTER should succeed | RED |
| Red char | `char(4)` | `中中中` | `char(3)` | direct target accepts value, ALTER should succeed | RED |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

Compare the DDL precheck result against the target type's own write semantics. If direct insertion into the target schema accepts the exact value, then a metadata-changing DDL whose purpose is only to ensure existing rows fit that target should not reject it.

## Why The Method Worked

The source did not say "multibyte strings are risky." The source gave a precise proof obligation:

```text
SELECT old_col FROM db.tbl
WHERE LENGTH(old_col) > new_flen
LIMIT 1
```

That immediately asks:

```text
What does `new_flen` measure for the target type?
What does `LENGTH` measure for the stored value?
```

For utf8mb4 `CHAR`/`VARCHAR`, those units differ. This compresses the search space to a two-axis matrix:

```text
ASCII vs multibyte
direct target acceptance vs ALTER precheck
```

No concurrency, partitioning, or random data generation was needed.

## Quality

Medium.

- User-visible symptom: a valid DDL is rejected with `ERROR 1265`.
- Strong oracle: direct target table accepts the same value.
- Root cause localized: `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:879`.
- It is not data loss or wrong result, so lower severity than id600001.
- It is still high methodology value because it demonstrates a new DDL selector that stays in the user's requested scope.

## Pause Gate

Do not enumerate every character set or every string type after this hit. Reopen S10 only when one of these changes:

- another DDL precheck uses a lossy metric for the target contract;
- the consequence changes from false rejection to silent wrong metadata/data behavior;
- a fix needs validation for binary-vs-non-binary boundaries, indexes, or char/varchar conversion.

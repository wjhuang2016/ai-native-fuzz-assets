# Method Case id30022: Backend not-found is not SQL failure

## Why This Selector Worked

This hit reused the S3 shortcut/extractor discipline, but changed the semantic dimension:

```text
code checks P: region_id predicate can be pushed into a PD point lookup
system believes Q: backend point lookup is equivalent to SQL filtering
fast path F: original predicate is removed, PD error is returned directly
missing D_dim: backend object-not-found must be empty SQL rowset, not execution failure
```

The AI-efficient step was not to enumerate system tables. It was to ask, for each shortcut:

```text
What does the backend API consider an error?
Would SQL users consider the same condition "no matching row"?
Can CASE wrapping force the safe scalar path?
```

`TIKV_REGION_PEERS` had exactly that shape. `region_id=0` is a valid SQL predicate over a `BIGINT` column, but the backend point lookup treats region 0 as an invalid/missing PD object and returns an HTTP error.

## Minimal Matrix

```text
RED  missing id:
     WHERE region_id = 0
     fast: PD 400 error
     CASE reference: 0 rows

RED  mixed id list:
     WHERE region_id IN (0, existing_region)
     fast: PD 400 error
     CASE reference: rows for existing_region

GREEN existing id:
     WHERE region_id = existing_region
     fast/reference both return the same rows

GREEN sibling key:
     WHERE store_id = 0
     returns 0 rows, no error
```

## Quality

This is a good bug because it is user-visible without failpoints and has a low-noise oracle:

- ordinary SQL predicate returns an error;
- CASE-wrapped reference proves the predicate result should be empty or non-empty;
- existing-region control proves the table and PD path are otherwise healthy;
- store-id control shows this is not "all missing backend filters error".

Severity is medium: it does not corrupt data, but it turns normal diagnostic filtering into errors and can hide valid rows in `IN` queries when one id is stale.

## Methodology Update

Add a new S3 sub-rule:

```text
external point lookup shortcut:
  object-not-found from the backend is often an empty SQL rowset, not a query error.
  IN-list lookups must handle each id independently; one missing id must not abort valid ids.
```

This extends `D_dims` with "backend error domain vs SQL predicate domain". It is different from the previous S3 collation/time/interval hits: the filter value is not mis-normalized; the error contract of the delegated backend API is too strong for SQL filtering.

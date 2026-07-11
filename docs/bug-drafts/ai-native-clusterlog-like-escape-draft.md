# cluster_log LIKE with custom ESCAPE can drop matching log rows

## Status

- `found_bug id30030`
- Status: `confirmed`
- Severity: `medium`
- Oracle: `O4_SCALAR_RECHECK_DIFFERENTIAL`
- Method: `S3_SHORTCUT_EXTRACTOR_LOSSY_PREFILTER`
- Testbed: `8192975`, `fp-tidb` via local port `14000`

## User-visible symptom

`information_schema.cluster_log` can return an empty result for a `LIKE ... ESCAPE` predicate
even when many log rows satisfy the predicate under normal SQL semantics.

The minimized shape is:

```sql
SELECT COUNT(*)
FROM information_schema.cluster_log
WHERE time >= '2026-07-02 13:47:08'
  AND time <  '2026-07-03 13:47:08'
  AND message LIKE '%#_%' ESCAPE '#';
```

On the testbed this fast arm returned `0`, while the scalar-recheck reference below returned
`130683` matching rows.

```sql
SELECT COUNT(*)
FROM information_schema.cluster_log
WHERE time >= '2026-07-02 13:47:08'
  AND time <  '2026-07-03 13:47:08'
  AND message LIKE '%'
  AND (CASE WHEN message LIKE '%#_%' ESCAPE '#' THEN 1 ELSE 0 END) = 1;
```

## Why this is a product bug

SQL semantics are settled by ordinary scalar evaluation:

```sql
SELECT
  'gc_service.go' LIKE '%#_%' ESCAPE '#' AS custom_escape_matches_underscore,
  'gc_service.go' LIKE '%#_%' AS default_escape_no_match,
  'gc_service.go' LIKE '%\_%' AS default_escape_matches_underscore;
```

Result:

```text
custom_escape_matches_underscore  default_escape_no_match  default_escape_matches_underscore
1                                 0                        1
```

An ordinary user table also behaves correctly:

```sql
CREATE TABLE t(msg VARCHAR(64));
INSERT INTO t VALUES ('gc_service.go'), ('abc#x'), ('plain');

SELECT GROUP_CONCAT(msg ORDER BY msg) AS rows_seen
FROM t
WHERE msg LIKE '%#_%' ESCAPE '#';

SELECT GROUP_CONCAT(msg ORDER BY msg) AS rows_seen
FROM t
WHERE msg LIKE '%#_%';
```

Result:

```text
custom ESCAPE:  gc_service.go
default ESCAPE: abc#x
```

So the scalar layer understands custom `ESCAPE`; the wrong result is specific to the
`cluster_log` shortcut extractor.

## Trigger evidence

The fast plan consumes the message predicate into the memtable scan:

```sql
EXPLAIN FORMAT='brief'
SELECT COUNT(*)
FROM information_schema.cluster_log
WHERE time >= '2026-07-02 13:47:08'
  AND time <  '2026-07-03 13:47:08'
  AND message LIKE '%#_%' ESCAPE '#';
```

Observed plan shape:

```text
MemTableScan table:CLUSTER_LOG start_time:2026-07-02 21:47:08, end_time:2026-07-03 21:47:07.999
```

No `Selection` remains for `message LIKE '%#_%' ESCAPE '#'`.

The reference plan keeps scalar evaluation:

```text
Selection eq(case(like(Column#5, "%#_%", 35), 1, 0), 1)
```

Observed result:

```text
arm                     cnt     self_true
fast                    0       NULL
ref                     130683  130683
default_escape_control  0       NULL
```

The default-escape control is green:

```text
fast_default_escape  130759  130759
ref_default_escape   130759  130759
```

This isolates the bug to custom `ESCAPE`, not to all `LIKE` pushdown.

## Source chain

- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:439-466`
  extracts `LIKE` on cluster log message.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:182-231`
  extracts only the column and the pattern constant; it ignores the third `ESCAPE` argument.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:463`
  compiles the extracted pattern with `stringutil.CompileLike2Regexp`.
- `/Users/bba/pc/tidb/pkg/util/stringutil/string_util.go:260-263`
  uses `CompilePattern(str, '\\')`, hardcoding the default backslash escape.

The planner then drops the original predicate from the remaining scalar filters, so the wrong
default-escape regexp becomes authoritative.

## Fix direction

Either:

1. preserve and pass the actual `ESCAPE` character into the regex compiler; or
2. only extract `LIKE` predicates when the escape is the default backslash, and keep scalar
   recheck for all custom-escape cases.

The fix validation should include:

- custom escape `'#'`: fast and CASE-reference agree;
- default escape `'\\'`: current green path stays green;
- no shortcut / scalar ordinary table: unchanged contract;
- plan trigger evidence: custom escape is either preserved in the extractor or left as scalar
  `Selection`.

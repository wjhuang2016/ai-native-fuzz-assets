# cluster_log REGEXP_LIKE ignores match_type and misses case-insensitive matches

## Status

- `found_bug id30033`
- Status: `confirmed`
- Severity: `medium`
- Oracle: `CASE scalar reference plus row-level self predicate`
- Method: `S3_OPERATOR_SEMANTIC_ARITY`
- Testbed: `8192975`, `fp-tidb` via local port `14000`

## User-visible symptom

`information_schema.cluster_log` can miss log rows when a user asks for a case-insensitive
regexp match using `REGEXP_LIKE(..., 'i')`.

The minimized fast arm was:

```sql
SELECT COUNT(*) AS cnt,
       COALESCE(SUM(REGEXP_LIKE(message,'GC_SERVICE.GO','i')),0) AS self_true
FROM information_schema.cluster_log
WHERE time >= '2026-07-02 14:54:59'
  AND time <  '2026-07-03 14:54:59'
  AND message LIKE '%'
  AND REGEXP_LIKE(message,'GC_SERVICE.GO','i');
```

Observed:

```text
cnt  self_true
0    0
```

The scalar-reference query over the same time window returned matching rows:

```sql
SELECT COUNT(*) AS cnt,
       COALESCE(SUM(REGEXP_LIKE(message,'GC_SERVICE.GO','i')),0) AS self_true
FROM information_schema.cluster_log
WHERE time >= '2026-07-02 14:54:59'
  AND time <  '2026-07-03 14:54:59'
  AND message LIKE '%'
  AND CASE WHEN REGEXP_LIKE(message,'GC_SERVICE.GO','i') THEN 1 ELSE 0 END = 1;
```

Observed:

```text
cnt    self_true
35742  35742
```

## Why this is a product bug

The `MESSAGE` column is `utf8mb4_bin`, so case sensitivity matters. TiDB scalar evaluation
honors `match_type`:

```sql
SELECT
  REGEXP_LIKE(_utf8mb4'gc_service.go' COLLATE utf8mb4_bin,'GC_SERVICE.GO') AS bin_default,
  REGEXP_LIKE(_utf8mb4'gc_service.go' COLLATE utf8mb4_bin,'GC_SERVICE.GO','i') AS bin_i,
  REGEXP_LIKE(_utf8mb4'gc_service.go' COLLATE utf8mb4_bin,'GC_SERVICE.GO','c') AS bin_c;
```

Result:

```text
bin_default  bin_i  bin_c
0            1      0
```

Controls over `cluster_log` were green:

```text
uppercase pattern + match_type='c':
  fast 0, reference 0

lowercase pattern + match_type='c':
  fast 35744, reference 35744
```

The failing case is specifically the omitted `match_type='i'` input, not regexp matching in
general.

## Trigger evidence

The fast plan has no remaining scalar `Selection` above `CLUSTER_LOG`:

```text
HashAgg
└─MemTableScan table:CLUSTER_LOG start_time:2026-07-02 22:54:59, end_time:2026-07-03 22:54:58.999
```

The reference plan keeps scalar evaluation:

```text
Selection eq(case(regexp_like(Column#5, "GC_SERVICE.GO", "i"), 1, 0), 1)
```

Sample returned by the reference:

```text
time                     type  level  ci  cs  sample
2026/07/02 14:55:00.978  pd    WARN   1   0   [gc_service.go:103] ["deprecated API GetGCSafePoint is called"]
```

## Source root cause

`pkg/planner/core/memtable_predicate_extractor.go` treats `ast.RegexpLike` as a pattern extractor:

```text
extractLikePattern -> extractColBinaryOpConsExpr -> datums[0].GetString()
```

The helper reads the column and one constant pattern, but does not preserve the third
`REGEXP_LIKE` argument `match_type`. The original scalar predicate is then removed from
`remained`, so the backend regexp request is trusted as if it preserved the full SQL operator.

## Fix direction

Do not extract `REGEXP_LIKE` when a non-default `match_type` is present unless the backend request
can carry equivalent regexp flags. Otherwise keep the original scalar predicate as a remaining
condition. Regression coverage should include:

- `utf8mb4_bin` message + uppercase pattern + `match_type='i'` red case;
- uppercase pattern + `match_type='c'` green control;
- lowercase pattern + `match_type='c'` green control.

## Method value

This is not "another cluster_log regexp variant." It validates the improved S3 selector:

```text
code extracts part of a scalar operator
system believes it extracted the whole operator
fast path drops scalar recheck
red cell is the omitted semantic input that flips truth value
```

After LIKE `ESCAPE`, the next useful step was to list `REGEXP_LIKE` semantic inputs and check
which ones the extractor records. `match_type` was the missing input.

## Stop rule

Do not enumerate regexp flags, pattern spellings, or other `cluster_log.message` regexp forms.
Reopen only for a different replacement mechanism, another omitted semantic-input family, or fix
validation.

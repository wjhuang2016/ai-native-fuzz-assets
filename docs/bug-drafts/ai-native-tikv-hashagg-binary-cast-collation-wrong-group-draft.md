# TiKV HashAgg can silently merge distinct `BINARY` groups

Status: confirmed on current TiDB master with real TiKV. No exact upstream issue or PR was found in
the post-RED duplicate search.

## Summary

`GROUP BY BINARY column` can produce fewer groups when TiDB pushes partial HashAgg to TiKV. Values
that are bytewise distinct by case or trailing spaces are merged using the connection collation.
The same expression evaluated and grouped at the TiDB root produces the correct bytewise groups.

An ordinary `INSERT ... SELECT ... GROUP BY` persists the wrong aggregate into a destination table.
The statement succeeds without a warning or consistency error.

## Production trigger

Applications commonly use `BINARY` when a source column has a case-insensitive or PAD SPACE
collation but a report, billing key, tenant key, or deduplication job requires bytewise identity.

The bug needs:

1. a string column;
2. two values that differ by case or trailing spaces;
3. `GROUP BY BINARY column` or `GROUP BY CAST(column AS BINARY(...))`;
4. a plan with partial HashAgg in TiKV.

It does not need concurrency, retries, failpoints, multiple TiDB nodes, MDL disabled, unusual SQL
variables, or an infrastructure fault.

## Environment

```text
TiDB master: 05b396fb6636f73b3bc06b09107cf43f2c725c35
TiKV source: 91ccfb212677a43fd5255183ccf2afa4e3cec23e
TiKV runtime: 730be34f959185c934b7d3db730ca1dbeb3949f8
Topology: one TiDB, one PD, one real TiKV
MDL: enabled
sql_mode: default strict mode
```

The relevant TiKV expression and executor source is unchanged between the runtime and current
TiKV master.

## Minimal reproduction

```sql
CREATE DATABASE repro;
USE repro;

CREATE TABLE t (
  id INT PRIMARY KEY,
  v TEXT
);

INSERT INTO t VALUES
  (1, ''),
  (2, ' '),
  (3, 'a'),
  (4, 'A'),
  (5, 'a ');

SELECT BINARY v AS k, COUNT(*) AS n
FROM t
GROUP BY BINARY v
ORDER BY k;
```

Actual pushed result:

```text
k    n
''   2
'a'  3
```

The five inputs are bytewise distinct. A type-preserving root barrier gives the expected result:

```sql
SELECT k, COUNT(*) AS n
FROM (
  SELECT BINARY v AS k
  FROM t
  LIMIT 18446744073709551615
) x
GROUP BY k
ORDER BY k;
```

```text
k     n
''    1
' '   1
'A'   1
'a'   1
'a '  1
```

`EXPLAIN` for the first query contains:

```text
HashAgg cop[tikv] group by:cast(repro.t.v, binary(1))
```

`EXPLAIN ANALYZE` confirms that the TiKV HashAgg itself emits only two groups. The final TiDB
HashAgg cannot recover groups that the partial aggregate already merged.

## Durable consequence

```sql
CREATE TABLE pushed_summary (
  k VARBINARY(64) PRIMARY KEY,
  n BIGINT NOT NULL
);

INSERT INTO pushed_summary
SELECT BINARY v, COUNT(*)
FROM t
GROUP BY BINARY v;

SELECT HEX(k), n FROM pushed_summary ORDER BY k;
```

The successful statement permanently stores only:

```text
HEX(k)  n
        2
61      3
```

Running the same insert through the root barrier stores five rows with count 1. Both summaries total
five source rows, so a row-count or sum-only check misses the corruption of the group partition.

## Root cause

The `BINARY` operator constructs a binary return type. However,
`newBaseBuiltinCastFunc4String` treats only an explicit charset clause as explicit collation state.
The normal path calls `deriveCollation`, whose `ast.Cast` branch assigns the connection charset and
collation to every string cast.

When the scalar function is serialized, `expr_to_pb.go` forcibly overwrites the protobuf return
type's collation with `expr.CharsetAndCollation()`. TiKV HashAgg therefore receives the connection
collation instead of the binary return type's collation and hashes groups case- and
space-insensitively.

The relevant ownership chain is:

```text
BINARY target FieldType is binary
  -> cast builtin derives connection collation
  -> ExprToPB overwrites RetType collation
  -> TiKV partial HashAgg partitions by the wrong equality relation
  -> TiDB final HashAgg receives already-merged groups
```

## Counterfactual

A temporary current-master build changed the string-cast constructor to preserve the target
charset/collation when either:

```text
isExplicitCharset || target charset is binary
```

With no SQL or plan change, both `BINARY v` and `CAST(v AS BINARY(64))` remained pushed and produced
five groups matching the root oracle. The source change was then removed and the worktree restored
cleanly.

## Expected behavior

A binary cast must carry binary equality and ordering semantics across the protobuf boundary.
Pushed partial aggregation and TiDB-root aggregation must produce the same group partition.

## Impact

Queries silently merge distinct business keys. Reporting, billing, tenant accounting, and ETL
deduplication can return or persist wrong aggregates. The result looks internally plausible because
the total count is preserved; only a group-partition oracle exposes the loss of identity.

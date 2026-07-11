# id30040: join_key_type_cast drops INT-VARCHAR join matches for scientific notation strings

Status: confirmed on testbed `8192975`; inserted into remote `found_bug` as id30040.

## User-Visible Symptom

A normal equality join between an `INT` column and a `VARCHAR` column can silently miss matching
rows when the string uses scientific notation.

In TiDB/MySQL numeric comparison semantics:

```sql
SELECT 10 = '1e1';
```

returns `1`. But with the `join_key_type_cast` optimizer rule enabled, this join omits the
`s='1e1'` row:

```sql
SELECT ti.id, ti.tag, tv.s, tv.info
FROM ti JOIN tv ON ti.id = tv.s
ORDER BY ti.id, tv.s;
```

Observed default result:

```text
1  one  1     plain1
2  two  2e0   sci2
10 ten  10    plain10
10 ten  10.0  tenfloat
```

Expected/reference result includes:

```text
10 ten  1e1   sci10
```

This is a wrong-result bug: the query succeeds and returns a stable rowset, but one SQL-visible
match is missing.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_jktc;
CREATE DATABASE ai_jktc;
USE ai_jktc;

CREATE TABLE ti(id INT PRIMARY KEY, tag VARCHAR(20));
CREATE TABLE tv(s VARCHAR(20) PRIMARY KEY, info VARCHAR(20));

INSERT INTO ti VALUES (1,'one'),(2,'two'),(10,'ten');
INSERT INTO tv VALUES
  ('1','plain1'),
  ('10','plain10'),
  ('10.0','tenfloat'),
  ('1e1','sci10'),
  ('2e0','sci2'),
  ('1.5','frac'),
  ('x','x');

SELECT 10 = '1e1' AS mysql_eq,
       CAST('1e1' AS DOUBLE) AS dbl,
       CAST('1e1' AS SIGNED) AS signed_val,
       CAST(CAST('1e1' AS SIGNED) AS DOUBLE) = CAST('1e1' AS DOUBLE) AS rule_guard;

SELECT GROUP_CONCAT(CONCAT(ti.id,':',tv.s) ORDER BY ti.id, tv.s SEPARATOR ',') AS pairs
FROM ti JOIN tv ON ti.id = tv.s;

SELECT GROUP_CONCAT(CONCAT(ti.id,':',tv.s) ORDER BY ti.id, tv.s SEPARATOR ',') AS pairs
FROM ti JOIN tv ON CASE WHEN ti.id = tv.s THEN 1 ELSE 0 END = 1;

INSERT IGNORE INTO mysql.opt_rule_blacklist VALUES ('join_key_type_cast');
ADMIN RELOAD OPT_RULE_BLACKLIST;

SELECT GROUP_CONCAT(CONCAT(ti.id,':',tv.s) ORDER BY ti.id, tv.s SEPARATOR ',') AS pairs
FROM ti JOIN tv ON ti.id = tv.s;

DELETE FROM mysql.opt_rule_blacklist WHERE name='join_key_type_cast';
ADMIN RELOAD OPT_RULE_BLACKLIST;
```

Observed on testbed:

```text
scalar contract:
mysql_eq=1, dbl=10, signed_val=1, rule_guard=0

default rule:
1:1,2:2e0,10:10,10:10.0

CASE oracle:
1:1,2:2e0,10:10,10:10.0,10:1e1

blacklist oracle:
1:1,2:2e0,10:10,10:10.0,10:1e1
```

## Trigger Evidence

`EXPLAIN FORMAT='brief'` for the default query shows the rule fired:

```text
HashJoin inner join, equal:[eq(Column#1, Column#13)]
Projection(Build): tv.s, tv.info, cast(tv.s, bigint BINARY)->Column#13
Selection:
  eq(cast(cast(tv.s, bigint BINARY), double BINARY), cast(tv.s, double BINARY))
```

After blacklisting `join_key_type_cast`, the plan keeps the original DOUBLE-domain equality:

```text
HashJoin inner join, equal:[eq(Column#9, Column#10)]
Projection(Build): cast(ti.id, double BINARY)->Column#9
Projection(Probe): cast(tv.s, double BINARY)->Column#10
```

So this is not a plan-only suspicion. The trigger fired, and the safe/reference arms return the
missing row.

## Source Anchors

- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_join_key_type_cast.go:28-47` documents the rule:
  it rewrites `CAST(int_col AS DOUBLE) = CAST(varchar_col AS DOUBLE)` into INT equality plus a
  VARCHAR-side guard.
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_join_key_type_cast.go:202-227` appends
  `CAST(varchar_col AS SIGNED)` and inserts the guard
  `CAST(CAST(varchar_col AS SIGNED) AS DOUBLE) = CAST(varchar_col AS DOUBLE)`.
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_join_key_type_cast.go:302-325` allows signed INT
  vs string columns, excluding unsigned and BIGINT but not excluding non-canonical numeric strings
  such as scientific notation.

## Root Cause

```text
P_check:
  The optimizer sees a DOUBLE equality produced from INT-vs-STRING casts, and checks that the
  string survives signed-int round-trip:
    CAST(CAST(varchar AS SIGNED) AS DOUBLE) = CAST(varchar AS DOUBLE)

Q_claim:
  After that guard, comparing INT keys is equivalent to the original mixed INT/VARCHAR equality.

D_dim:
  MySQL/TiDB numeric string comparison accepts scientific notation in the DOUBLE/numeric
  comparison domain. But `CAST('1e1' AS SIGNED)` returns integer prefix `1`, while
  `CAST('1e1' AS DOUBLE)` returns `10`.

F_effect:
  The rule filters out `s='1e1'` before the join and then compares INT-domain keys, so a row that
  should match `id=10` never reaches the join result.
```

## Fix Direction

The rewrite must preserve the original comparison domain, not only an integer-prefix domain.
Possible safe directions:

- keep a scalar recheck of the original equality after the INT-key join;
- apply the rewrite only when the string side is proven integer-canonical under the same semantics
  used by the original comparison;
- or replace the guard with a predicate that proves equivalence to the original mixed comparison
  for every accepted string.

The fix-validation contract should include:

```text
RED before fix:
  '1e1' joins with INT 10 under the original scalar comparison but is dropped by the rule.

GREEN controls:
  canonical integers ('1', '10') still match;
  decimal integer-valued strings ('10.0') still match if the original comparison matches;
  fractional strings ('1.5') do not incorrectly match INT 1;
  nonnumeric strings do not match;
  BIGINT / unsigned guard behavior remains on the intended original path.
```

## Method Lesson

This is the compact form of the P/Q/F method:

```text
source proves "string can be represented as INT"
system believes "original mixed comparison can be replaced by INT equality"
optimizer takes the fast path and drops the original comparison domain
oracle compares fast path with CASE/blacklist reference
```

The high-yield selector is not "try weird strings". It is:

```text
when optimizer/executor replaces a general semantic domain with a narrower domain,
list the domain dimensions the replacement must preserve, then find the smallest value where the
domains disagree.
```

Here the missing dimension was numeric-string grammar: `DOUBLE` parsing accepts scientific notation,
while signed integer parsing uses only the integer prefix.

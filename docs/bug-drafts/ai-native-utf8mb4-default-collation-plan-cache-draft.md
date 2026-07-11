# Draft: prepared plan cache reuses `_utf8mb4` literal collation after `default_collation_for_utf8mb4` changes
> 2026-07-03. Confirmed on testbed 8192975 / `fp-tidb`. Inserted into remote
> `found_bug` as id30037.

## Summary

Prepared plan cache can reuse the collation assigned to `_utf8mb4` string literals after the session
changes `@@default_collation_for_utf8mb4`.

Direct SQL follows the current session setting. A prepared statement first built under
`utf8mb4_bin` keeps binary literal collation after switching to `utf8mb4_general_ci`, so equality
and row filtering can be wrong until the session plan cache is flushed.

## Minimal Repro

Direct contract:

```sql
SET @@default_collation_for_utf8mb4 = 'utf8mb4_bin';
SELECT
  COLLATION(_utf8mb4'A') AS lit_coll,
  _utf8mb4'A' = _utf8mb4'a' AS eq;
-- utf8mb4_bin, 0

SET @@default_collation_for_utf8mb4 = 'utf8mb4_general_ci';
SELECT
  COLLATION(_utf8mb4'A') AS lit_coll,
  _utf8mb4'A' = _utf8mb4'a' AS eq;
-- utf8mb4_general_ci, 1
```

Projection form:

```sql
SET tidb_enable_prepared_plan_cache = 1;

SET @@default_collation_for_utf8mb4 = 'utf8mb4_bin';
PREPARE s FROM
  'SELECT COLLATION(_utf8mb4''A'') AS coll,
          (_utf8mb4''A'' = _utf8mb4''a'') AS eq';
EXECUTE s; -- utf8mb4_bin, 0
SELECT @@last_plan_from_cache; -- 0

SET @@default_collation_for_utf8mb4 = 'utf8mb4_general_ci';
EXECUTE s; -- wrong: utf8mb4_bin, 0
SELECT
  @@last_plan_from_cache,
  (SELECT COLLATION(_utf8mb4'A')) AS direct_coll,
  (SELECT _utf8mb4'A' = _utf8mb4'a') AS direct_eq;
-- last_plan_from_cache = 1, direct_coll = utf8mb4_general_ci, direct_eq = 1

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s; -- utf8mb4_general_ci, 1
SELECT @@last_plan_from_cache; -- 0
```

Row-filtering form:

```sql
DROP DATABASE IF EXISTS ai_utf8mb4_coll_0703;
CREATE DATABASE ai_utf8mb4_coll_0703;
USE ai_utf8mb4_coll_0703;
CREATE TABLE t(id INT PRIMARY KEY);
INSERT INTO t VALUES (1), (2);

SET tidb_enable_prepared_plan_cache = 1;
SET @@default_collation_for_utf8mb4 = 'utf8mb4_bin';

PREPARE q FROM
  'SELECT COUNT(*) AS cnt FROM t WHERE _utf8mb4''A'' = _utf8mb4''a''';

EXECUTE q; -- cnt = 0
SELECT @@last_plan_from_cache; -- 0

SET @@default_collation_for_utf8mb4 = 'utf8mb4_general_ci';

EXECUTE q; -- wrong: cnt = 0
SELECT
  @@last_plan_from_cache,
  (SELECT COUNT(*) FROM t WHERE _utf8mb4'A' = _utf8mb4'a') AS direct_cnt;
-- last_plan_from_cache = 1, direct_cnt = 2

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE q; -- cnt = 2
SELECT @@last_plan_from_cache; -- 0
```

## Control

Explicit `COLLATE` is green:

```sql
SET @@default_collation_for_utf8mb4 = 'utf8mb4_bin';
PREPARE s FROM
  'SELECT _utf8mb4''A'' COLLATE utf8mb4_general_ci =
          _utf8mb4''a'' COLLATE utf8mb4_general_ci';
EXECUTE s; -- 1

SET @@default_collation_for_utf8mb4 = 'utf8mb4_general_ci';
EXECUTE s; -- 1, cache hit
```

## Source Proof

- `pkg/planner/core/expression_rewriter.go:1660-1663`: underscore-charset UTF8MB4 literals get
  `er.sctx.GetDefaultCollationForUTF8MB4()` written into the field type.
- `pkg/sessionctx/variable/sysvar.go:1987-1995`: `default_collation_for_utf8mb4` is session/global
  and updates `SessionVars.DefaultCollationForUTF8MB4`; setting it emits warning 1681 on this
  build, but still succeeds.
- `pkg/sessionctx/variable/varsutil.go:65-73`: only allowed default UTF8MB4 collations are accepted.
- `pkg/planner/core/plan_cache_utils.go:390-438`: prepared plan-cache key includes connection
  charset/collation, but not `default_collation_for_utf8mb4`.

## Method Value

This is the first hit after switching S7 from function-name enumeration to getter-level scanning.
The selector found a new hidden input immediately:

```text
GetDefaultCollationForUTF8MB4
+ expression rewrite writes the value into literal type metadata
+ plan-cache key omits that session variable
+ cache hit reuses old literal collation
+ direct/cache-hit/flush oracle exposes row-count difference
```

It also refines the "key-covered" rule. Connection collation being in the cache key is not enough:
`default_collation_for_utf8mb4` is a different session variable with different consumers.

## Quality

Medium. The bug is a wrong-result under prepared plan cache. It can turn a true case-insensitive
constant predicate into a cached false predicate and drop all rows.

## Stop Rule

Do not enumerate all charset literal spellings. Reopen only for another hidden input, a different
literal/type payload owner, or fix validation.

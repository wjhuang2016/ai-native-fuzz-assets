# Method Case: id30037 `_utf8mb4` literal collation after `default_collation_for_utf8mb4`

## What We Were Testing

id30037 is the first direct validation of the getter-level S7 workflow after id30036.

The proof card:

```text
P_check:  prepared plan-cache key says the cached expression plan can be reused
Q_claim:  cached underscore-charset literal collation remains valid after session default changes
F_effect: cache hit reuses literal field-type metadata written during expression rewrite
D_dim:    rewrite reads @@default_collation_for_utf8mb4
```

## Small Matrix

```text
direct default=utf8mb4_bin:
  COLLATION(_utf8mb4'A') -> utf8mb4_bin
  _utf8mb4'A' = _utf8mb4'a' -> 0

direct default=utf8mb4_general_ci:
  COLLATION(_utf8mb4'A') -> utf8mb4_general_ci
  _utf8mb4'A' = _utf8mb4'a' -> 1

prepare under bin, execute -> utf8mb4_bin / 0, cache=0
switch to general_ci, same EXECUTE -> utf8mb4_bin / 0, cache=1 RED
direct under general_ci -> utf8mb4_general_ci / 1
flush session plan cache, same EXECUTE -> utf8mb4_general_ci / 1, cache=0 GREEN reference

row consequence:
WHERE _utf8mb4'A' = _utf8mb4'a' after bin -> general_ci
cached count 0 vs direct count 2 RED
after flush count 2 GREEN reference

explicit COLLATE control:
_utf8mb4'A' COLLATE utf8mb4_general_ci =
_utf8mb4'a' COLLATE utf8mb4_general_ci -> 1 before and after switch
```

## Why This Was Fast

The new workflow said to scan getters, not functions. `GetDefaultCollationForUTF8MB4` had the right
shape:

- It is a session variable getter.
- It is consumed during expression rewrite, before the cached expression tree is stored.
- The consumer writes durable type metadata into `_utf8mb4` literals.
- The prepared plan-cache key covers connection collation, but not this variable.
- The direct old/new contract is just `COLLATION()` plus equality.

That reduced the live test to one projection matrix and one row-count matrix.

## Selector Improvement

id30037 prevents a subtle false comfort:

```text
connection charset/collation is key-covered
therefore collation-sensitive expressions are covered
```

That implication is false. The correct proof is per hidden input and per consumer. A session variable
can look adjacent to a key-covered dimension while still being absent from the key and still writing
cached payload.

Updated S7 scan question:

```text
Which exact getter is read?
Which exact payload field is written?
Is that getter's value represented in the cache key, explicit SQL, or a rebuilt boundary?
Can direct old/new SQL produce a two-row semantic contract?
```

## Stop Rule

This is a representative literal-collation payload case. Do not enumerate every charset introducer
or comparison spelling. Reopen only for a new hidden input, a different payload owner, or fix
validation.

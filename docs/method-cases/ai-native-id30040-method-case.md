# Method Case: id30040

## Classification

- Selector: S20 `semantic-domain rewrite narrowing`
- Oracle: O25 `join-domain reference`
- Root cause id: `join-key-type-cast-domain-narrowing`
- Counting: new root cause, not blast radius of an existing selector
- Quality: medium. It is a silent wrong-result in ordinary SELECT JOIN, with a small repro and
  two strong references: CASE-wrapped scalar comparison and disabling the optimizer rule.

## Why This Worked

This was found by reading the proof obligation in the optimizer rule, not by fuzzing string values.

The source comment and rewrite say:

```text
original comparison:
  CAST(int_col AS DOUBLE) = CAST(varchar_col AS DOUBLE)

fast path:
  int_col = CAST(varchar_col AS SIGNED)

guard:
  CAST(CAST(varchar_col AS SIGNED) AS DOUBLE) = CAST(varchar_col AS DOUBLE)
```

That immediately creates a proof question:

```text
Does "signed-int round trip equals double parse" prove that the original mixed comparison is
equivalent to integer equality?
```

The smallest counterexample family is not random strings; it is strings whose numeric grammar is
accepted differently by the two target domains. Scientific notation is the compact red cell:

```text
10 = '1e1'                         -> true
CAST('1e1' AS DOUBLE)              -> 10
CAST('1e1' AS SIGNED)              -> 1
signed-int round-trip guard         -> false
```

## The Small Matrix

| Cell | Expected under original comparison | Default rule | Reference |
| --- | --- | --- | --- |
| `'1'` vs `1` | match | match | CASE/blacklist match |
| `'2e0'` vs `2` | match | match | CASE/blacklist match |
| `'10.0'` vs `10` | match | match | CASE/blacklist match |
| `'1e1'` vs `10` | match | **missing** | CASE/blacklist match |
| `'1.5'` vs `1` | no match | no match | no match |
| `'x'` | no match | no match | no match |

The red cell is intentionally tiny: one string where the old comparison domain and new guard
domain disagree.

## Oracle

Two references made the verdict strong:

```text
CASE oracle:
  FROM ti JOIN tv ON CASE WHEN ti.id = tv.s THEN 1 ELSE 0 END = 1

rule-disabled oracle:
  INSERT IGNORE INTO mysql.opt_rule_blacklist VALUES ('join_key_type_cast');
  ADMIN RELOAD OPT_RULE_BLACKLIST;
  FROM ti JOIN tv ON ti.id = tv.s
```

Both references return `10:1e1`; the default optimized query does not. `EXPLAIN FORMAT='brief'`
shows the default plan inserted the guard and rewrote the join key, while the rule-disabled plan
kept DOUBLE-domain equality.

## Improvement To The Method

Add this as a first-class target shape:

```text
semantic-domain rewrite:
  general domain D_old is replaced by cheaper/narrower domain D_new
  code checks a local guard P
  system believes D_new preserves D_old for all accepted values
```

The efficient workflow is:

1. Name both domains explicitly.
2. List the parser/equality/null/collation/overflow dimensions that differ.
3. Pick the smallest value where `D_old` and `D_new` disagree.
4. Use a safe path that forces the original scalar comparison, not only a plan diff.

This improves the earlier P/Q/F card by making `D_dims` concrete. Without naming `DOUBLE parse`
vs `SIGNED integer parse`, the case looks like string fuzzing. Once the domains are named, `'1e1'`
is the obvious first red cell.

## Stop Rule

Do not enumerate numeric string spellings. Reopen only for:

- a different rewrite that replaces one semantic domain with another;
- a stronger consequence, such as wrong rows added rather than missed;
- or fix validation of the `join_key_type_cast` rule across canonical integer, decimal,
  scientific notation, fractional, nonnumeric, unsigned, and BIGINT boundaries.

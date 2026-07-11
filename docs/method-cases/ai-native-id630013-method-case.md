# id630013 Method Case: Reorg Writes Must Re-Prove Row Invariants

## What Worked

This bug came from the same high-level pattern as the recent DDL wins, but with a different owner:

```text
old state satisfies invariant
DDL rewrites state through a special path
special path assumes the invariant still holds
normal safe path would have rechecked it
```

The important move was to stop treating `ALTER TABLE ... MODIFY COLUMN` as a schema-only validator
problem. For type changes, it is also a data-rewrite operator. That makes row-level constraints part
of the proof obligation.

## Selector

```text
S17_DDL_REORG_CONSTRAINT_BYPASS

Find DDL reorg/backfill code that:
1. decodes existing rows,
2. transforms one or more values,
3. writes rows through a low-level path,
4. bypasses the normal DML invariant checks.
```

For id630013, the source contrast was very sharp:

```text
normal DML path:     TableCommon.AddRecord/UpdateRecord -> CheckRowConstraint
ADD CHECK path:      verifyRemainRecordsForCheckConstraint
MODIFY reorg path:   CastColumnValue -> EncodeRow -> txn.Set
```

That contrast gave the test matrix directly.

## Small Matrix

```text
old type     row before ALTER     CHECK before     ALTER to     row after ALTER     CHECK after
DECIMAL      0.40                 true             INT          0                   false
DOUBLE       0.4                  true             INT          0                   false
VARCHAR      '0.4'                true             INT          0                   false
```

Controls:

```text
ADD CHECK on INT data containing 0 -> ERROR 3819
INSERT 0 into the altered table -> ERROR 3819
SHOW WARNINGS immediately after ALTER -> empty
```

The matrix stayed small because the D dimension was not "all type conversions"; it was specifically
"a successful conversion that changes the CHECK truth value". Fractional positive values rounding
or truncating to zero are the cheapest witness.

## Why It Found a Real Bug Quickly

Random DDL fuzzing would have had to guess the combination of CHECK, fractional values, and a
lossy but non-erroring type conversion. The proof-obligation route almost wrote the seed for us:

- CHECK says row predicate must hold.
- MODIFY type reorg changes row values.
- The reorg code does not call the DML CHECK evaluator.
- Therefore choose data where old predicate is true and converted predicate is false.

The strong oracle avoided ambiguity:

- We did not rely on "I think ALTER should reject this".
- `ADD CHECK` has an explicit existing-row verifier and rejects the final data.
- Ordinary DML rejects the final bad value too.
- The altered table nevertheless contains that value.

## Stop Rule

Do not enumerate every source/target type pair. This selector should reopen only when there is a
new invariant owner on a raw reorg path:

- CHECK constraints on another DDL rewrite path.
- Partition membership after a row-moving conversion.
- Generated/hidden column values when a reorg writes encoded rows directly.
- Foreign-key, placement, TTL, or masking invariants if the reorg path writes around their normal
  validator.

The gating question is always:

```text
Does the special DDL writer transform row or metadata state and then bypass the normal safe-path
invariant check?
```

If yes, build the smallest old-valid/new-invalid witness and use the safe path as oracle.

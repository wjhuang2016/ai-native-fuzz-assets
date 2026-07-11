# Method Case id30016: validator-backed restore selector
> 2026-07-03. FK `FLASHBACK TABLE` missing parent. This note records the methodology result, not the bug details.

## What Was Being Tested

After user correction, the lane was narrowed back to DDL-only. The question was:

```text
Can S6 (restore path re-materializes historical metadata) still find new bugs
without drifting into executor/query modules?
```

The first version of S6 after id30011 was too broad:

```text
restore/undelete/import path copies old metadata verbatim
```

That would tempt enumeration: placement, TTL, cache, TiFlash, FK, BR, import, and so on. The improved selector adds a stronger filter:

```text
normal create/alter path has an explicit validator
+ recover path skips that validator
+ recovered state has a behavior oracle, not only a metadata diff
```

## Timeline

1. **Boundary screen first.**
   TTL recover was green because source explicitly disables TTL scheduling on recover table/schema. Table-cache recover was blocked because cached tables cannot be dropped. TiFlash recover looked static-high-signal, but the testbed has no TiFlash store/PD placement target, so runtime proof would be noisy.
2. **Pick FK because it has a create-time validator.**
   Normal `CREATE TABLE ... FOREIGN KEY` goes through `checkTableForeignKeyValidInOwner`. `RecoverTable` only checks target DB/name/ID and GC safe point, then clones old `TableInfo`.
3. **Tiny matrix.**
   Three cells were enough:
   - red candidate: drop child, drop parent, flashback child;
   - green control: drop database, flashback database with parent+child together;
   - green control: ordinary create child with missing parent must fail.
4. **Strong oracle.**
   The red cell needed all three:
   - metadata says FK exists (`SHOW CREATE`, `key_column_usage`);
   - behavior accepts orphan rows while parent is missing;
   - plan evidence shows no `Foreign_Key_Check` until parent is recreated.

## Why It Worked

The key was not "try more FK DDL." FK rename/drop/index paths had already been covered as green in the object-reference probe. The new dimension was **recover bypasses the ordinary validator**.

This is the refined proof shape:

```text
P_check:
  recover checks target DB/name/ID and safe point

Q_claim:
  old TableInfo is valid as current schema metadata

D_dim:
  referenced parent table still exists and FK enforcement can be planned

F_effect:
  recover publishes old ForeignKeys directly
```

The red input makes `P_check` pass while `D_dim` is false.

## Quality

High. The bug is user-visible and not only metadata cosmetic:

- `foreign_key_checks=ON`;
- SQL-visible schema says FK exists;
- invalid DML succeeds during the missing-parent window;
- parent recreation re-enables checks only for later writes, leaving old orphan data.

It is also not a duplicate of id30011. id30011 is DB-level placement metadata causing future DDL failure. id30016 is table-level FK metadata causing DML constraint bypass.

## Methodology Improvement

S6 should be scored by validator gap, not restored-field count:

```text
score += normal path has explicit validator
score += recover path does not call it
score += validator protects a user-visible behavior
score += behavior oracle can distinguish metadata-only from real enforcement
score -= field is deliberately disabled/stripped on recover
score -= environment cannot instantiate the referenced resource
```

This gives better precision than broad restore fuzzing. It also explains the green/blocked samples instead of treating them as failed attempts.

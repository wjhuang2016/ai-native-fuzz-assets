# id30005 Method Case: sequence defaults and DDL object references

## Goal

Use the sequence-default red cells as a complete AI-native DDL bug-discovery case.

This case is not about adding more sequence tests. It is about validating and improving this selector:

```text
executable schema expression references a separate DDL object
+ create/alter validates that object
+ runtime resolves it by name
+ remove/rename/container DDL lacks a reverse dependency scan
= dangling schema expression after DDL
```

## Why AI Could Mark This Cell Before Running It

The high-signal facts were visible from the code:

1. `DEFAULT NEXT VALUE FOR seq` is stored inside table metadata as restored SQL text.
2. The parser/preprocess path treats the name in `nextval(seq)` as a sequence object and validates it during create/alter.
3. Runtime evaluation later resolves the same string by schema and sequence name.
4. `DROP SEQUENCE` only verifies that the target object is a sequence.
5. `RENAME TABLE seq TO seq2` already has owner-specific rewrite hooks for FK and masking-policy metadata, but no equivalent hook for sequence defaults.
6. `DROP DATABASE` has a schema-level FK referred check and masking-policy cleanup, but no cross-schema sequence-default dependency check.

So the predicted red cells were:

```text
table default references sequence
+ DDL removes or renames the sequence through a path without a reverse scan
= table default still points at old sequence name
```

## Oracle

This is a DDL reference-ownership oracle:

```text
after sequence-object DDL:
  SHOW CREATE TABLE must not expose a default pointing at a missing sequence
  default INSERT must either still work or the sequence DDL must have blocked
```

The green controls matter:

- live sequence default insert consumes the sequence;
- `CHANGE COLUMN ... DEFAULT NEXT VALUE FOR seq` keeps the default live when the sequence object is unchanged.

Those controls prove the finding is not "sequence defaults are generally broken". The broken edge is specifically DDL that changes or removes the referenced sequence object.

## Result

Probe:

```text
/Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

Observed result:

```text
SUMMARY total=5 findings=3 skipped=0
```

The findings are one root family:

```text
DROP SEQUENCE, sequence rename, and cross-DB DROP DATABASE all succeed,
but the live table default continues to point at the old sequence name.
The next default insert fails with ERROR 1146.
```

Issue/repro draft:

```text
/Users/bba/pc/ai-native-sequence-default-reference-draft.md
```

## Fix Semantics

The current evidence favors block-first semantics.

Recommended behavior:

```text
DROP SEQUENCE seq
=> block if any live column default references seq
```

Reason:

- dropping the sequence cannot rewrite a dependent default to an equivalent live object;
- silently removing or nulling the default would change table semantics;
- recreating the same sequence name later is an accidental repair, not a valid DDL guarantee.

For rename there are two valid directions:

```text
RENAME TABLE old_seq TO new_seq
=> block while referenced
```

or:

```text
RENAME TABLE old_seq TO new_seq
=> rewrite dependent defaults to the new qualified name
```

Rewrite is closer to FK and masking-policy rename behavior, but block is smaller and safer because sequence defaults are stored as expression text and can be cross-schema.

For cross-schema drop:

```text
DROP DATABASE seq_db
=> block if it removes a sequence referenced by a live table outside seq_db
```

If the dependent table is inside the same dropped schema, there is no external live reference left after the drop.

## Method Lesson

This case improves the DDL reference methodology in three ways.

First, it expands "reference owner" beyond sys tables and side metadata:

```text
column default expression
can also be a reference owner
if it names a separate DDL object and is executed later
```

Second, it separates useful expression owners from low-signal `SHOW CREATE` hints:

```text
SQL-visible metadata alone is weak
SQL-visible metadata + runtime object lookup is strong
```

Region split policy is the negative example: it is object-local `TableInfo` or `IndexInfo` metadata, so rename/drop naturally carries or removes it. Sequence default is different because it points to another independently dropped/renamed object.

Third, it gives the next scan a sharper source-reading recipe:

```text
find expressions persisted in schema metadata
then ask:
  does preprocess validate a named object?
  does runtime look that object up again by name or ID?
  do all DDL paths that change the object call a reverse scan, rewrite, cleanup, or block helper?
```

## Next Owner Search

Use id30005 to search for new DDL-only owners, not for more sequence variants.

Prioritize owners with all of these:

1. the reference is stored in table, column, index, partition, or side metadata;
2. the reference names or identifies another DDL object;
3. create/alter checks that the target object exists or has the right type;
4. later user-visible behavior reuses the stored reference;
5. at least one remove, rename, move, truncate, or drop-schema path appears not to call the owner helper;
6. the oracle can be expressed as "DDL must block, rewrite, or leave future behavior working".

Downweight owners when:

- the metadata is object-local and moves/drops with the same `TableInfo` or `IndexInfo`;
- the metadata is name-bound policy by design;
- the oracle depends mainly on slow background cleanup;
- the only observable consequence is pure optimizer/executor behavior unrelated to DDL-created metadata.

## Stop Rule

Do not continue sequence-default fuzzing unless one of these happens:

1. owner feedback asks for a specific variant;
2. a fix is proposed and needs validation;
3. source changes add a new sequence DDL entrypoint or a dependency-tracking helper.

Otherwise, the next move is a new DDL owner selected by the refined expression-reference selector above.

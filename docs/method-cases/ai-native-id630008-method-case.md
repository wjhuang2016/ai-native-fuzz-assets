# id630008 Method Case: DDL Idempotence Flag Dropped

## Selector

```text
S15_DDL_IDEMPOTENCE_FLAG_DROPPED
```

This case starts from sibling DDL branches that share one grammar feature but do not propagate the
same execution flag.

```text
P_check:  parser accepts and records IF NOT EXISTS on ADD FOREIGN KEY
Q_claim:  duplicate existing object should be treated as idempotent success
effect:   FK branch calls CreateForeignKey without the IfNotExists bit
D_dim:    sibling ADD INDEX / ADD COLUMNAR paths pass the bit and convert duplicates to notes
```

## Matrix

| Cell | SQL shape | Oracle | Result |
| --- | --- | --- | --- |
| First FK add | `ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS` | FK appears once | GREEN |
| Duplicate FK add with IFNE | same statement again | should be idempotent | RED, ERROR 1826 |
| Plain duplicate FK add | `ADD CONSTRAINT fk_pid FOREIGN KEY` | duplicate should fail | GREEN reject |
| Sibling index IFNE | `ADD INDEX IF NOT EXISTS idx_a(a)` | note, unchanged schema | GREEN |
| FK schema count | `information_schema.referential_constraints` | exactly one FK | GREEN, no duplicate write |

## Oracle

```text
O18_IDEMPOTENT_DDL_FLAG_ORACLE
```

If a DDL syntax accepts `IF [NOT] EXISTS`, the existing-object or missing-object case should be
classified through that idempotence flag, not through the hard-error path used when the flag is
absent. A sibling DDL kind that already implements the same flag is a strong control.

## Why The Method Worked

The source code made the proof obligation explicit:

```text
ADD INDEX:
  createIndex(..., constr.IfNotExists)

ADD FOREIGN KEY:
  comment says IF NOT EXISTS is ignored
  CreateForeignKey(...)
```

That gave a two-by-two matrix:

```text
flag absent + duplicate -> hard error
flag present + duplicate -> should not hard error
sibling index + flag present -> note
schema count -> no duplicate side effect
```

## Quality

Low to medium severity, high selector clarity.

- It is a wrong-error/idempotence bug, not a data bug.
- The user-facing symptom is easy to understand: rerunning an idempotent migration step fails.
- The selector is reusable for other DDL feature flags: parser bit exists, one sibling passes it,
  another sibling silently drops it.

## Pause Gate

Do not enumerate every FK option. Reopen S15 only for:

- another DDL idempotence flag with a different object owner;
- a silent wrong-acceptance or duplicate-write consequence;
- fix validation across FK name, missing parent/index/column errors, and sibling index behavior.

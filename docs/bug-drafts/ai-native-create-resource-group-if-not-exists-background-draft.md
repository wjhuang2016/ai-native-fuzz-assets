# CREATE RESOURCE GROUP IF NOT EXISTS pre-validates unused BACKGROUND options

## Summary

`CREATE RESOURCE GROUP IF NOT EXISTS` can still fail when the named resource group already exists.
The failing branch is not a duplicate-write bug; it is a wrong-error caused by building and
rejecting the unused candidate definition before the `IF NOT EXISTS` duplicate classifier runs.

Remote `found_bug`: id630020, confirmed.

## User-visible Symptom

A migration or bootstrap script that is meant to be rerunnable can fail on a resource group that is
already present:

```sql
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=();
```

On an existing non-default resource group, this returns:

```text
ERROR 1105 (HY000): unsupported operation. Currently, only the default resource group support change background settings
```

The resource group is unchanged, but the statement aborts instead of becoming the expected
idempotent no-op.

## Minimal Repro

Environment: testbed `8192975`, TiDB `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`,
`tidb_enable_resource_control=ON`.

```sql
DROP RESOURCE GROUP IF EXISTS ai_rg_s15;
DROP RESOURCE GROUP IF EXISTS ai_rg_s15_absent;

CREATE RESOURCE GROUP ai_rg_s15 RU_PER_SEC=1000;
SHOW CREATE RESOURCE GROUP ai_rg_s15;

-- Green duplicate control.
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000;
SHOW WARNINGS;
SHOW CREATE RESOURCE GROUP ai_rg_s15;

-- Red cell: target exists, but unused BACKGROUND option is rejected.
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=();
SHOW WARNINGS;
SHOW CREATE RESOURCE GROUP ai_rg_s15;

-- Target-absent control: the same bad candidate should still hard-error.
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15_absent BACKGROUND=();
SHOW WARNINGS;

SELECT NAME, RU_PER_SEC, PRIORITY
FROM information_schema.resource_groups
WHERE NAME IN ('ai_rg_s15','ai_rg_s15_absent')
ORDER BY NAME;
```

Observed:

```text
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000
  -> Query OK, Note 8248 Resource group 'ai_rg_s15' already exists
  -> SHOW CREATE remains RU_PER_SEC=1000

CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=()
  -> ERROR 1105 unsupported operation...
  -> SHOW CREATE remains RU_PER_SEC=1000

CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15_absent BACKGROUND=()
  -> ERROR 1105 unsupported operation...
  -> no ai_rg_s15_absent row exists
```

## Expected

When the named resource group already exists and `IF NOT EXISTS` is present, TiDB should classify
the statement as a duplicate no-op and append Note 8248 before building or rejecting candidate
options that will not be used.

When the resource group is absent, the same `BACKGROUND` option should still hard-error.

## Root Cause

Source anchors:

- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6350`: `AddResourceGroup`
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6353`: calls `buildResourceGroup` before existence
  classification.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6358`: only later checks `ResourceGroupByName`.
- `/Users/bba/pc/tidb/pkg/ddl/resource_group.go:185`: `buildResourceGroup`
- `/Users/bba/pc/tidb/pkg/ddl/resource_group.go:244`: `ResourceGroupBackground`
- `/Users/bba/pc/tidb/pkg/ddl/resource_group.go:245`: rejects `BACKGROUND` for non-default groups.

The effective order is:

```text
CREATE RESOURCE GROUP IF NOT EXISTS rg BACKGROUND=()
  -> buildResourceGroup(candidate)
  -> SetDirectResourceGroupSettings(BACKGROUND)
  -> error because rg != default
  -> never reaches existing-rg IF NOT EXISTS note path
```

The idempotence classifier exists, but it is dominated by an earlier candidate build-time option
gate.

## Quality

Severity: low.

This is a real user-visible wrong-error for rerunnable DDL scripts. It does not corrupt data or
metadata, and the existing resource group stays unchanged. Its value is mainly methodological: it
extends the S15 create-like selector beyond table/sequence owners into a non-table DDL owner.

## Method Lesson

This was found by reusing the same proof obligation that produced id630018 and id630019:

```text
code validates candidate definition
system assumes candidate may be used
fast path returns before target-exists no-op
missing dimension: target object already exists, so candidate is discarded
```

The improvement from this case is that the early gate can live inside a builder, not only inside an
obvious validator. For future S15 scans, inspect:

- source resolvers,
- metadata builders,
- option setters,
- semantic validators,
- special-name helpers,
- capability gates,
- raw request count checks.

Boundary checks from the same scan:

- `CREATE VIEW IF NOT EXISTS` is not a bug in this build because the grammar does not accept that
  form.
- `CREATE PLACEMENT POLICY IF NOT EXISTS` was a green control for this sub-shape because policy
  existence is checked before `checkPolicyValidation`.

Stop rule: do not enumerate resource-group options. Reopen only for a different idempotent DDL
owner, a stronger consequence than wrong-error, or fix validation.

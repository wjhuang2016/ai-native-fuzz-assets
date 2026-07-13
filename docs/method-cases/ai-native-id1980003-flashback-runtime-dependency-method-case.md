# id1980003: Restore the dependency closure, not only the primary object

## Starting Point

Current source defines the Flashback restore domain by key ranges. User schemas and table metadata
are restored, while most `mysql` system tables are explicitly excluded. Instead of enumerating all
excluded rows, the pass asked which restored metadata fields have mandatory runtime consumers in an
excluded owner.

`TableCacheStatusEnable` immediately stood out: NOCACHE deletes an ID-keyed row from
`mysql.table_cache_meta`, and cached-table DML requires that row at commit.

## P / Q / F

```text
P: A restored TableInfo marked CACHED ON has exactly one usable table_cache_meta row.
Q: Restoring user metadata while excluding mysql state preserves that dependency.
F: NOCACHE after the target TSO deletes the row; FLASHBACK restores only TableInfo; DML commit fails.
```

## Small Matrix

| Cell | Metadata | Side row | Highest consumer | Result |
| --- | --- | --- | --- | --- |
| normal CACHE | enabled | present | INSERT commit | GREEN |
| owner split simulation | enabled | absent | SELECT | GREEN fallback |
| owner split simulation | enabled | absent | INSERT commit | RED |
| actual FLASHBACK | restored enabled | absent | fresh INSERT | RED |
| single-owner compensation | enabled | restored | INSERT commit | GREEN |

The read/write split was important. A metadata-only check would prove drift but overstate impact;
`SELECT` alone would miss the severe consequence. Following the field to the commit owner produced
the direct availability oracle.

## Method Improvement

Add `RESTORE_DOMAIN_COVERS_RUNTIME_DEPENDENCIES`:

1. Enumerate what the restore includes and excludes.
2. For each restored capability/state bit, trace its highest mandatory consumer.
3. Find required side owners outside the restore domain.
4. Use a natural mutation between target time and restore to remove or replace one side owner.
5. Observe the terminal consumer, then restore only that owner as the counterfactual.

This is stronger than scanning for special object types. Ordinary tables can acquire special runtime
dependencies through feature state. It also avoids a false lead seen in the same pass: a mock made
Flashback split retry look like a hot loop by deleting client-go's lower-layer backoff, while the
product contract explicitly says Flashback retries until success.

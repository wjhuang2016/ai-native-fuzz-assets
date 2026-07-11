# id30009 Method Case: ADD GLOBAL INDEX rollback delete-range prefix

## Selector

Common path is green, sibling rollback path has a distinct ownership reconstruction step:

- success/drop global index cleanup uses table ID
- rollback rebuilds `ModifyIndexArgs` after removing index metadata
- delete-range cleanup later depends on `IndexArg.IsGlobal`

The proof obligation is:

```text
For a global index, every cleanup range must use table ID, not partition physical IDs.
```

## Small Matrix

| Case | Shape | Oracle | Result |
| --- | --- | --- | --- |
| Red | partition table, failed `ADD UNIQUE INDEX gu(c) GLOBAL` | decode `mysql.gc_delete_range.start_key` | partition IDs used |
| Red+ | partition table, cancelled `ADD INDEX g(c) GLOBAL` after KV is written | raw TiKV scan | table-ID global index KV remains |
| Green | successful `ADD INDEX g(c) GLOBAL`, then `DROP INDEX g` | decode delete range | table ID used |
| Green | failed local `ADD UNIQUE INDEX lu(p,c)` | decode delete range | partition IDs used |

## Strong Oracle

Do not rely on `ADMIN CHECK TABLE` alone. The failed rollback removes schema metadata, so table checks can pass while storage cleanup is wrong.

The first useful oracle is:

```text
decode(gc_delete_range.start_key).tableID == tableID for global index cleanup
decode(gc_delete_range.start_key).tableID in partitionIDs for local index cleanup
```

The confirming oracle is:

```text
after rollback, SHOW CREATE TABLE has no index;
raw scan(tableID, global indexID) finds keys;
raw scan(partitionIDs, registered delete ranges) finds no keys.
```

On testbed `8192975`, job `13195` confirmed this with table ID `13190`, partition IDs `13191/13192/13193`: six logical global-index keys remained under table ID `13190`, while the registered partition-ID origin/temp ranges were empty.

## Why This Worked

This is the same method pattern as id30007, but applied to add-index cleanup:

1. Start from source-level proof obligation.
2. Compress it into a 3-row matrix.
3. Use a low-noise structural oracle instead of broad fuzzing.
4. Pause after the first red row and run only green controls that explain the red.

The key improvement over the previous add-index attempts was to stop looking only at successful rowset consistency. The bug lives in a failed DDL cleanup path, where user table semantics are already restored but side metadata points at the wrong physical keyspace. The final strengthening was to force rollback after real KV write, turning a structural metadata mismatch into observable orphan storage.

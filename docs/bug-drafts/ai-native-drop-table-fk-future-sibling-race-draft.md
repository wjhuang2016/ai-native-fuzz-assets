# Parent-first batch DROP TABLE can leave persistent foreign-key orphans

Status: confirmed high severity / critical persistent-integrity consequence on current master and
official nightly with MDL and foreign-key checks enabled.

## Summary

TiDB accepts a multi-object `DROP TABLE` after checking foreign-key references against the complete
list of table names. It assumes every listed child table will disappear with the parent.

The statement is then executed as a sequence of separately committed DDL jobs. If another ordinary
DDL renames a future child after the parent job commits but before the child job starts, both
statements can return success:

- the parent is permanently dropped;
- the renamed child survives with its original `REFERENCES parent(...)` metadata;
- while the parent is missing, foreign-key checks allow new orphan rows;
- recreating a same-name parent protects only later writes, so existing orphans persist;
- `ADMIN CHECK TABLE` remains green.

## Production trigger

This schedule can occur during overlapping schema deployments:

1. A cleanup or migration tool submits one parent-first `DROP TABLE IF EXISTS p, ..., c`.
2. The list contains a real parent and its child, so TiDB admits the parent drop on the promise that
   the child will be dropped later in the same SQL statement.
3. After the parent job commits, another deployment, online migration tool, or operator renames the
   child table.
4. The original statement reaches the old child name, records an `IF EXISTS` note, and returns
   success.
5. Application traffic reaches the renamed child before the parent is recreated.

Large cleanup lists, slow DDL ownership, metadata synchronization, or concurrent migration systems
make the window wider. The reproducer uses filler tables only to make this ordinary interleaving
deterministic. It does not require a failpoint, source change, process pause, node failure, disabled
MDL, disabled foreign-key checks, or nondefault transaction isolation.

## Reproduction

Run the reusable RED/GREEN harness:

```bash
MYSQL_PORT=4000 \
  scaffolds/top-level/ai_native_drop_table_fk_future_sibling_race.sh
```

The RED changes the parent-child list order only:

```sql
DROP TABLE IF EXISTS p, f01, ..., f80, c;
```

The concurrent session waits until `p` disappears and runs:

```sql
RENAME TABLE c TO c_survivor;
```

The matched GREEN drops `c` first:

```sql
DROP TABLE IF EXISTS c, f01, ..., f80, p;
```

## Actual result

On current master `231dad5225`:

```text
parent-first DROP: success with Note 1051 for old child name
concurrent RENAME: success
surviving child: c_survivor
declared parent: p
parent immediately after DROP: absent
orphan rows after one ordinary INSERT: 2
ADMIN CHECK TABLE c_survivor: green
```

After recreating `p` with only key `999`, child rows `(10,1)` and `(11,1)` remain orphaned. A new
child `(12,999)` succeeds, and a new child `(13,1)` is rejected with 1452. This proves that checks
are active and the historical orphans are durable.

The child-first control removes the child before the parent disappears. The concurrent rename gets
1146, and no survivor or orphan remains.

## Expected result

The parent must not become durably absent while any child that justified its drop admission can
survive. TiDB can satisfy this by making the batch atomic, revalidating the remaining child
identities before parent publication, or making each parent job ignore only children already
removed by an earlier committed job.

## Root cause

`dropTableObject` builds `objectIdents` from the whole user list and uses it for every foreign-key
precheck. `checkTableHasForeignKeyReferred` skips a reference whenever the child name appears in that
list.

The same full list is attached to every `DropTableArgs`, so the DDL owner repeats the same
future-child exemption. Execution then calls `doDDLJob2` once per table and commits each job before
starting the next one. The check proves only a planned future name, while the irreversible parent
drop relies on the stronger claim that the same child identity is already removed or cannot move.

## Strong oracle

The oracle joins:

1. success or terminal status of both DDL statements;
2. parent absence after the parent job;
3. surviving child object identity and `SHOW CREATE TABLE` FK metadata;
4. an orphan anti-join before and after same-name parent recreation;
5. successful valid and rejected invalid post-recreation writes;
6. matched child-first ordering;
7. `ADMIN CHECK TABLE` as a deliberately weak structural control.

## Deduplication

Post-RED searches for `DROP TABLE + foreign key + RENAME TABLE`, `batch drop table foreign key`, and
`orphan rows foreign key drop table` found no matching TiDB issue. The bug database contains nearby
FK roots, but none use a non-atomic batch admission promise about a future sibling:

- `id2820003` freezes the reference graph inside multi-table rename;
- `id1500002` rebinds a flashed-back child to a same-name parent;
- `id2490003` loses rollback closure during FK cascade.

## Fix direction

Do not pass the complete future object-name list as an exemption to each independently committed
drop job. A safe implementation should bind exemptions to immutable table identities and to jobs
that are already complete or atomically coupled. Before publishing the parent drop, revalidate that
every referring child is absent in the latest InfoSchema or is covered by the same atomic action.

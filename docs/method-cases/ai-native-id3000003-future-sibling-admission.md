# id3000003: future sibling effects cannot justify an earlier irreversible action

Status: validated by current-master and nightly RED plus a one-dimension child-first GREEN.

## Proof obligation

```text
P: child c appears later in the same batch request.
Q: c will be absent when dropping parent p becomes durable.
F: p and c are independent committed jobs, and c can be renamed before its job starts.
```

The invariant is:

```text
An earlier irreversible action may depend only on effects that are already durable, identity-bound,
or guaranteed by the same atomic action.
```

## Small matrix

| Batch order | Concurrent child rename | Result |
| --- | --- | --- |
| parent, fillers, child | after parent commit | both DDLs succeed; child and orphans survive |
| child, fillers, parent | after parent commit | child already absent; rename fails; no orphan |

The filler count changes only scheduling width. Parent/child order is the semantic dimension.

## Selector

```text
candidate = batch precheck uses the complete requested sibling set
            intersect execution commits siblings independently and sequentially
            intersect an earlier job relies on a later sibling's promised effect
            intersect sibling identity or membership can change between jobs
            intersect earlier effect is irreversible
            minus atomic batch action or per-job latest-state revalidation
```

Use the name `FUTURE_SIBLING_EFFECT_AS_ADMISSION_PROOF`.

## Why this worked

The source-level proof mismatch was compact:

1. admission checks used every requested object name;
2. every object received a separate DDL job;
3. each job carried the same complete ignore list;
4. the ignored object was identified by a mutable name;
5. `IF EXISTS` converted the stale final lookup into successful completion.

The test then needed only one adversarial schedule: mutate the future sibling after the earlier
irreversible boundary. The matched GREEN moved that sibling before the boundary without changing
the schema or concurrency.

## Consumer lifting

Stopping at dangling FK metadata understates the consequence. The missing-parent interval is a
consumer:

```text
future-child promise invalidated
  -> parent drop remains committed
  -> renamed child survives with dangling FK
  -> missing-parent FK check admits ordinary inserts
  -> same-name parent recreation does not repair historical rows
  -> persistent relational corruption
```

## Loop improvement

For every batch API:

1. distinguish batch admission from batch atomicity;
2. list which sibling effects each early item assumes;
3. mark each assumed sibling as past, current, or future;
4. bind proof to immutable identity, not only a mutable name;
5. mutate or cancel a future sibling immediately after the first irreversible boundary;
6. compare against the same sibling moved before that boundary;
7. lift metadata drift into the first durable consumer;
8. count one root per admission primitive and irreversible consumer.

This selector applies to DDL batches, backup/restore object lists, import manifests, cleanup queues,
multi-resource configuration changes, and background task groups.

## Stop rule

Table names, filler counts, rename destinations, child row values, and `IF EXISTS` warning text are
blast radius. Reopen only for a different batch admission primitive, identity boundary, or higher
irreversible consumer.

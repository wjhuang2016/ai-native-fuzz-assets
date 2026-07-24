# id2850003: async work must revalidate the live resource generation

Status: confirmed high-severity data-loss root on current TiDB master and a real NextGen CSE/TiKV
stack.

## Proof obligation

```text
P: the target table ID and metadata are valid when IMPORT INTO is planned and accepted.
Q: the same ID still owns a live target generation when a later worker performs irreversible writes.
F: a legal DDL retires or replaces the generation while the task crosses owners or waits in queue.
```

The required invariant is:

```text
Before the first irreversible write, an asynchronous task must prove that its persisted
resource generation is still live and authorized for this operation.
```

## Small matrix

| Schedule | Generation changes | Final oracle | Verdict |
| --- | ---: | --- | --- |
| Worker available, no DDL | no | job `finished`; rows in target | GREEN |
| Worker queued, old table renamed away | live object, new name | rows follow old ID | semantic calibration |
| Worker queued, atomic name swap | name points to new ID | success writes old object | RED diagnostic |
| Worker queued, `TRUNCATE TABLE` | old generation retired | success writes retired ID | RED, high admission |
| Persisted task, generation changed before `OnPrepare` | old generation retired/replaced | scheduler returns nil | RED owner proof |

The `TRUNCATE` row closes the ambiguity left by rename. A name can move with an object; a truncated
generation has no live SQL owner.

## Selector

Reuse `LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF` with a generation-retirement extension:

```text
candidate = asynchronous plan snapshots resource ID or metadata
            intersect work crosses transaction, keyspace, process, or queue owner
            intersect a legal operation can retire or replace that resource generation
            intersect execution trusts the persisted identity without a live lookup
            intersect irreversible write, delete, publish, or success acknowledgement
            minus generation lease, incompatible-operation fence, or fail-closed revalidation
```

For each candidate, draw:

```text
semantic target -> submission owner -> persisted handoff -> execution owner
                -> live generation lookup -> irreversible consumer
```

Any missing edge is a high-value test point.

## Strong oracle

Join control-plane truth with data-plane attribution:

1. public job and task terminal state;
2. reported affected row count;
3. submission-time and execution-time resource IDs;
4. rows visible through the current logical target;
5. physical rows under the retired ID;
6. cleanup ownership for the retired key range.

Job success plus row count is a weak oracle. `ADMIN CHECK TABLE` on the current generation is also
blind because the misplaced rows live outside its keyspace.

## Why this worked

The source exposed a temporal proof gap. TiDB checked the table once, persisted a complete
`TableInfo`, and later treated that frozen payload as execution authority. The cross-keyspace
handoff and CSE queue supplied a natural owner boundary. A two-row matrix then changed only the
target generation.

The first name-swap RED was useful but not sufficient. Escalating the mutation from name change to
generation retirement removed the semantic debate and produced a direct data-loss oracle.

## Improvement to the loop

After finding a stale resource identity:

1. classify mutations as rename, replacement, retirement, and reuse;
2. use rename only to locate the consumer;
3. require retirement or generation replacement for severe admission;
4. pause the execution owner instead of injecting a product error when queue delay is a legal state;
5. compare public success with both live-object and retired-keyspace attribution;
6. back-solve whether the missing guard belongs at submission, handoff, preparation, or the
   irreversible boundary.

This pass applies to imports, backup/restore, TTL, distributed DDL, statistics jobs, placement
updates, ingest pipelines, and cleanup workers.

## Stop rule

Count one root per persisted identity / generation fence / irreversible consumer tuple. More DDL
verbs, file formats, row counts, worker delays, and target names are blast radius. Reopen only when a
different owner needs its own generation proof or the consequence reaches a higher durable
consumer.


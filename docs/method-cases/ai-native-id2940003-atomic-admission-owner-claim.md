# id2940003: exclusive-owner checks need an atomic admission claim

Status: validated by natural current-master RED, deterministic mechanism RED, and a matched
single-owner GREEN.

## Proof obligation

```text
P: a read observes zero active jobs for target table T.
Q: the new job is the only import owner allowed to write T.
F: another request reads the same zero before either request publishes its ownership claim.
```

The invariant is:

```text
An exclusivity precheck and the corresponding owner publication must be one atomic operation.
```

## Small matrix

| Schedule | Product injection | Terminal and data oracle | Verdict |
| --- | --- | --- | --- |
| One detached import | none | finished; table/index 2/2; ADMIN green | GREEN |
| Two concurrent imports, forced after both zero reads | test-only barrier | both failed; table/index 2/4; 8223 | RED |
| Two concurrent imports | none | both admitted and failed; table/index 2/4; 8223 | RED, 3/3 |
| Classic, second request after mode is visible | none | second rejected 8258; first and table/index green | GREEN |
| Classic, concurrent 1M-row imports, default write speed | none | one finished, one failed; table/index 1M/2M; wrong lookup; 8223 | RED |

The natural rows are the severity result. The barrier row only proves the exact check-to-claim
interleaving. The Classic serialized control shows that table mode blocks a later owner but cannot
distinguish two owners that race its publication.

## Strong oracle

Join five observations:

1. admission cardinality: two job IDs and two DXF tasks exist for one target;
2. terminal truth: both jobs are failed with checksum mismatch;
3. primary owner view: hidden row handles and values from a table scan;
4. sibling owner view: the same handles and values through every affected index;
5. point truth: look up one value from each sibling input and compare the returned row;
6. structural truth: `ADMIN CHECK TABLE` and the reported handle/value mismatch.

The late job failure is not a safety success, and one sibling reporting `finished` is not table
success. Durable row/index disagreement remains after both terminal states.

## Selector

```text
candidate = count/existence/precondition read intended to prove exclusive ownership
            intersect ownership publication occurs later or in another transaction/keyspace
            intersect two admitted owners derive IDs, ranges, epochs, or artifacts independently
            intersect irreversible write/delete/publish
            intersect late validation cannot roll back the effect
            minus unique claim, lock, lease, fencing token, or atomic compare-and-create
```

Use the name `ATOMIC_ADMISSION_BEFORE_IRREVERSIBLE_PARALLEL_OWNERS`.

## Why this worked

The source-level race was easy to describe and easy to underestimate. The useful step was to trace
what two admitted owners generate independently:

- both importers saw the same empty-table state;
- both allocated hidden handles 1 and 2;
- each produced a record artifact and a unique-index artifact;
- record keys collided, while index keys remained distinct;
- checksum ran after physical ingest and had no rollback owner.

That trace converted a duplicate-job concern into a deterministic physical corruption oracle.
Disjoint logical inputs were important: they show that user data contained no duplicate values and
that the collision came from importer-owned identity allocation.

Reusing the same assets against Classic found a different missing guard: the system added
`TableModeImport`, but represented only the mode and not the owning job. This demonstrates the
incremental loop: preserve the oracle and scenario, replace one admission primitive, and test
whether the new primitive closes the same proof obligation.

## Loop improvement

For every "check P, therefore assume Q" candidate:

1. name the exclusive owner or resource that Q claims;
2. locate the exact transaction boundary between checking P and publishing the claim;
3. ask whether a second actor can independently pass the same check;
4. enumerate owner-local generated namespaces such as handles, ranges, epochs, filenames, or
   checkpoints;
5. choose disjoint logical inputs that collide only in a generated physical namespace;
6. inspect sibling durable artifacts separately, not only the main output;
7. join terminal state with post-terminal durable state;
8. run natural concurrency before introducing a scheduler or failpoint;
9. keep late checksum/validation as detection, not as proof of rollback.

Severity should be lifted in this order:

```text
two owners admitted
  -> both perform work
  -> one or both report a misleading terminal result
  -> irreversible residue remains
  -> sibling artifacts disagree
  -> fresh reads return wrong results or structural checks fail
```

This selector applies across import, restore, repair, distributed DDL, background cleanup, TTL,
statistics, placement, and any queue that intends to enforce one active owner per resource.

## Stop rule

Count one root per missing atomic claim / generated namespace / irreversible consumer tuple.
Request counts, file formats, row values, object-store providers, indexes, and timing widths are
blast radius. Reopen only when another owner uses a different admission primitive or damages a
different durable invariant.

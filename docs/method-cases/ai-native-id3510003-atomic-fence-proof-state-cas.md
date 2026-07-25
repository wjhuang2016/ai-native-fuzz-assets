# id3510003: guard acquisition must atomically validate the proof state

## Starting proof obligation

```text
P: the target has schema S0, no active import exists, and the table is empty.
Q: acquiring TableMode later makes it safe to encode with S0.
F: schema changes to S1 after S0 is captured but before TableMode is acquired.
```

The important source shape is `check P -> do unguarded preparation -> acquire guard G`. A guard
that blocks future changes cannot prove that no change happened between the check and acquisition.

## Small matrix

| ADD UNIQUE INDEX timing | Import schema | TableMode claim | Persistent result |
| --- | --- | --- | --- |
| before planning | S1 | after S1 | row/index GREEN; duplicate rejected |
| after target resolution, exact barrier | S0 | after S1 | empty unique index; duplicate accepted |
| during natural 60k-entry discovery | S0 | after S1 | same RED, 3/3 |
| after TableMode acquisition | S0 | before DDL | DDL is rejected or waits; no mixed schema |

The first and second rows differ only in whether the schema transition occurs before or after the
plan captures its proof state.

## Strong oracle

1. Require both DDL and import to report success.
2. Require the import job to be `finished`.
3. Read records with `USE INDEX()`.
4. Read every current public index with `FORCE INDEX`.
5. attempt a duplicate write against the new unique index.
6. Run `ADMIN CHECK TABLE`.
7. Inspect local and remote checksum groups.

Job success, total row count, or the default checksum alone all miss this corruption.

## Selector improvement

Add `ATOMIC_FENCE_PROOF_STATE_CAS`:

1. Find a proof state captured before a later lock, lease, mode, epoch, or ownership claim.
2. List all preparation work between capture and claim.
3. Ask whether the claim validates the exact state or only blocks future mutation.
4. Put one legal mutation in the gap.
5. Drive the old proof state into an irreversible consumer.
6. Join public success with current-owner closure.
7. Use a before-capture GREEN and an after-claim negative cell.

High-value source shapes:

```text
snapshot := currentState()
prepare(snapshot)
guard.acquire()
publish(snapshot)

if precheck() {
    expensiveDiscovery()
    switchToProtectedMode()
    startWorkers(precheckResult)
}
```

## Why it worked

The TableMode feature looked like the safety mechanism, so a broad concurrency test would tend to
assume the protected interval began early enough. Ordering the owners exposed that the mode is
acquired only after file discovery and prechecks. The source itself selected the race window.

A tiny semantic matrix then separated three claims:

- the DDL and import are individually valid;
- the guard blocks future changes;
- the guard does not validate the past.

The persistent oracle upgraded an empty-index symptom into a unique-constraint violation. Inspecting
checksum groups also exposed a second reusable weakness: an absent required group can be an
additive identity, allowing self-referential validation to pass.

## Cross-module targets

- backup jobs that check a source generation before acquiring a backup lock;
- restore jobs that inspect an empty target before claiming restore mode;
- DDL reorg plans that snapshot schema before registering a write fence;
- GC jobs that select candidates before obtaining a retention lease;
- schedulers that validate capacity before atomically claiming an owner epoch;
- metadata publishers that validate a version before acquiring an external lease.

## Stop rule

Do not enumerate more index types or file counts after the same stale-schema claim is proved. Move
to another `(proof state, guard owner, irreversible consumer)` tuple. Retire a candidate when guard
acquisition atomically compares the complete proof token, or when an outer lock covers both capture
and irreversible use.

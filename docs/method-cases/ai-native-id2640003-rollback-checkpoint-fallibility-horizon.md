# id2640003: rollback checkpoints must cover the whole fallibility horizon

Remote `found_bug id2640003`, issue-filed high severity / critical consequence:
[TiDB #69838](https://github.com/pingcap/tidb/issues/69838).

## Starting proof obligation

TiDB creates an internal savepoint before an FK cascade because cascade execution needs intermediate
`StmtCommit` publication. The local assumption was:

```text
P: the savepoint can undo all parent and cascade stages.
Q: once the FK trigger itself succeeds, releasing the savepoint is safe.
F: the outer user statement still has a fallible final LockKeys phase.
```

`Q` does not follow from `P`. The checkpoint protects statement atomicity, so its lifetime must cover
every later error that can still become the user's statement result.

## Production-first card

| Field | Concrete witness |
| --- | --- |
| Supported workload | pessimistic tenant/account PK migration with `ON UPDATE CASCADE` and a no-op guard-row assignment |
| Natural producer | an older batch worker holds the guard >50s because of a long batch, hot Region, server-busy backoff, or storage pressure |
| Lifetime inequality | intermediate publication and savepoint release happen before final guard lock timeout |
| Topology | one TiDB, one TiKV, two sessions; no Region split or DDL |
| Public plus durable result | UPDATE returns 1205, later COMMIT succeeds, fresh state is `(2,2)` instead of `(1,1)` |

The application-side requirement is explicit: it catches 1205 as a retryable statement conflict and
commits the still-open transaction, for example to preserve earlier audit/progress work. An always-
rollback client is a non-triggering control.

## Small matrix

| Storage | Timeout | Savepoint lifetime | UPDATE | Fresh parent/child |
| --- | --- | --- | --- | --- |
| mock TiKV | 1s compressed | released after FK trigger | 1205 | **2/2** |
| real TiKV | 1s compressed | released after FK trigger | 1205 | **2/2** |
| real TiKV | default 50s | released after FK trigger | 1205 | **2/2** |
| mock TiKV | 1s compressed | retained through final locks | 1205 | 1/1 |
| real TiKV | 1s compressed | retained through final locks | 1205 | 1/1 |

The default-settings row matters: it proves the candidate is not created by shortening the lock timeout.

## Selector

Store `ROLLBACK_CHECKPOINT_FALLIBILITY_HORIZON` as:

```text
candidate = intermediate publication or irreversible staging
            intersect rollback checkpoint/savepoint ownership
            intersect checkpoint release
            intersect later fallible outer consumers
            minus equivalent rollback owner or whole-transaction fail-closed behavior
```

Candidate generation:

1. Enumerate internal savepoints, mem-buffer checkpoints, staging handles, undo logs, and compensation
   tokens.
2. Identify why each checkpoint exists and which intermediate effects it owns.
3. Find every release, commit, or ownership transfer point.
4. Continue the control-flow walk to the actual public terminal result. List later lock, validation,
   render, encode, cancellation, acknowledgment, and response consumers.
5. Generate one natural terminal failure after release and before public success.
6. If an explicit outer transaction survives, commit it and inspect fresh durable state. Internal stage
   counts are supporting evidence, not the oracle.
7. Retain only a checkpoint-lifetime counterfactual; broad transaction rollback can mask the owner.

## Why this found a new root

The earlier `INTERMEDIATE_PUBLICATION_LOCK_CLOSURE` selector asked whether published mutations retain
their later lock owner. This pass asked a different ownership question: whether those mutations retain
their later rollback owner. The same intermediate `StmtCommit` is background context, but the highest
consumer and the minimal correction differ.

This generalizes beyond FK cascades. Any nested operation that releases an undo capability when the
nested operation succeeds, while its enclosing user operation still has fallible finalizers, has the
same proof gap.

## Method improvement

Represent rollback protection as a scoped token:

```text
protects(checkpoint=C, effects=E, until=terminal boundary T)
```

Do not reduce it to `savepointCreated=true`. AI should compare `T` with every reachable public error
site. A release is admissible only when it post-dominates all fallible consumers or transfers `E` to an
equivalent rollback owner.

Stop after one root per checkpoint owner/release boundary/highest consumer. Different timeout values,
guard rows, child tables, or parent-key values are blast radius.

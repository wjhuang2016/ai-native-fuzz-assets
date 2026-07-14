# id2490003: intermediate publication can lose outer lock ownership

Remote bug DB: `found_bug id2490003`, confirmed high consequence.
Upstream: [TiDB #69828](https://github.com/pingcap/tidb/issues/69828), `severity/critical` and `found-by-ai`.

## Starting proof obligation

FK cascade needs the parent update to become visible inside the same transaction before the nested
cascade executor runs. TiDB satisfies that local obligation with `StmtCommit`. The stronger outer
obligation is: publishing state for an inner consumer must not discard the pessimistic lock ownership
needed to serialize the whole user statement.

The source-derived candidate was:

```text
inner consumer requires visible mutations
-> intermediate StmtCommit releases the current stage
-> cleanup creates a fresh stage
-> outer finalizer scans only the current stage for KeysNeedToLock
-> released mutations have no transferred owner
```

## Small matrix

The first schedule used a same-key conflict. Storage detected it at commit and returned an assertion
failure, so it did not prove silent corruption. The decisive matrix changed only the competitor to a
disjoint child-row key whose validity depends semantically on the old parent key.

| Owner operation | Competitor | Terminal oracle | Result |
| --- | --- | --- | --- |
| parent PK update plus cascade | same parent/key conflict | commit status | late error, no corruption |
| parent PK update plus cascade | new child referencing old parent | both commits plus FK anti-join | durable orphan |
| same as above, pre-release lock counterfactual | new child referencing old parent | error 1452 plus empty anti-join | safe |

This is why a semantic invariant and a disjoint physical key are essential. Same-key probes can be
caught by TiKV prewrite and hide a missing higher-level lock obligation.

## Method improvement

Add `INTERMEDIATE_PUBLICATION_LOCK_CLOSURE` to the candidate generator:

1. Enumerate `Publish`, `Release`, `Commit`, `Flush`, stage reset and generation rotation operations.
2. Find later `Lock`, `Validate`, or `Finalize` consumers that inspect only the current stage.
3. Subtract explicit owner transfer, union journals, or already-acquired lock caches.
4. Generate a competitor on a disjoint physical key tied to the released mutation by a semantic invariant.
5. Require both terminal operations to succeed, then check the invariant from a fresh session.

The useful signal is not merely “state was reset.” It is the asymmetry that the inner consumer sees
enough published state to proceed while the outer safety path has lost the proof obligation for that
same state.

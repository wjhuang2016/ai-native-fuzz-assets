# Method case: extend owner coherence to the runtime consumer

Remote `found_bug`: `id1830003`, issue-filed high: https://github.com/pingcap/tidb/issues/69785.

## Why this was not just another PD drift

S35 identified the same abstract ordering shape as id1710003 and id1800003, but metadata-versus-PD
comparison alone did not prove the proposed severity. The method therefore added the next owner:
the TiDB planner and TiFlash query path.

## Matrix

| Cell | Metadata | PD rule | TiFlash-only query | Verdict |
| --- | --- | --- | --- | --- |
| Cancel after rule deletion | count 1, available 1 | absent | 9012 timeout | RED |
| Restore committed rule | count 1, progress 1 | count 1 | 5/150 | GREEN |
| Normal replica removal | absent | absent | immediate 1815 | GREEN |

## Method improvement

For control-plane drift, enumerate consumers after durable owners:

```text
terminal result -> metadata owner -> external control plane -> physical state -> runtime consumer
```

Stop at the first layer that independently dominates the consequence. Here the runtime consumer did
not heal the drift: TiFlash-only planning accepted stale available metadata and timed out. That
promoted the candidate from a control-plane inconsistency to a high-quality availability bug.

## Environment lesson

The first TiFlash image was version-incompatible and a second registry name was not resolvable. The
matrix did not run until a manifest-verified master image produced a compatible 9.0 TiFlash store.
Capability validation is part of the oracle, not setup trivia.

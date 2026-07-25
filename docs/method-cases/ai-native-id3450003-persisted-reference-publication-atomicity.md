# id3450003: publish durable targets before durable references

## Starting proof obligation

Persistent workflows often publish a small manifest, migration, or checkpoint that points to a
larger object:

```text
reference R -> target T
```

The consumer assumes that observing `R` proves `T` exists. That proof is valid only when target
creation precedes reference publication, or when both changes share an atomic owner.

The selector is:

```text
durable reference publication
  + fallible target creation
  + process lifetime boundary between them
  + retry allocates a fresh target identity
  + future consumer traverses every historical reference
  -> persistent dangling-reference candidate
```

## Why source reading found it quickly

The useful unit was the owner sequence, not an individual error branch:

| Step | Durable owner | Retry identity | Consumer-visible |
| --- | --- | --- | --- |
| append migration | log-backup migration set | retained | yes |
| write extbackupmeta | per-restore object path | regenerated | yes, through migration |
| copy SSTs | per-restore object directory | regenerated | only through extbackupmeta |
| log restore | all historical migrations | n/a | fail-fast |

Writing this table exposed two facts before any broad test generation:

1. the reference was made durable before its target;
2. retry generated a new path, so success could not heal the old reference.

## Small matrix

| First attempt | Exit mode | Retry identity | Highest consumer | Verdict |
| --- | --- | --- | --- | --- |
| target and reference persist | graceful | same run | log restore | GREEN |
| reference persists, target fails | graceful close | same run repair | log restore | boundary/control |
| reference persists, target absent | abrupt exit | fresh path | log restore | RED |
| target persists, reference absent | abrupt exit | fresh path | log restore | GREEN/orphan only |

The important asymmetry is the last two rows. An orphan target costs storage; an orphan reference
breaks every consumer that requires referential closure.

## Strong oracle

After a failed attempt and a successful retry:

1. enumerate every durable reference, including historical attempts;
2. force the highest production consumer to traverse them;
3. require every referenced object to be readable;
4. require the retry to publish only reachable state;
5. run the same schedule after changing only the publication order.

Checking only that the retry succeeded is weak. Checking that the new path exists is also weak. The
old path is the failure owner.

## Counterfactual

Move target creation before reference publication:

```text
create T
  -> publish R(T)
```

This changes crash semantics from a consumer-visible dangling reference to an unreachable object.
The latter can be reclaimed asynchronously without blocking restore.

## Method improvement

Add `PERSISTED_REFERENCE_PUBLICATION_ATOMICITY` to source selection. Search for:

```text
manifest/path append
checkpoint/object registration
catalog/blob publication
queue/payload registration
index/segment manifest update
```

For each match, classify:

```text
reference owner
target owner
publication order
retry identity reuse or regeneration
historical reference retention
consumer missing-target policy
```

Prioritize candidates where retry changes identity and the consumer is fail-fast. A local rollback
or successful retry does not repair a reference owned by an older durable history.

## Severity discipline

Separate impact from trigger frequency:

```text
impact: persistent recovery-chain failure, potentially critical
reachability: abrupt exit inside two external writes, narrower
triage: high / critical-impact lane
```

Do not call the bug critical solely because the affected subsystem is backup. Promote it when a
production-shaped process-kill run confirms the window or when the same selector finds a
configuration-only or common-retry trigger.

## Stop rule

Stop after one `(reference owner, target owner, retry identity, highest consumer)` tuple is proven.
Path names, storage backends, restore scopes, and process-exit causes are blast-radius variants.
Transfer the selector to other modules and durable owner pairs.

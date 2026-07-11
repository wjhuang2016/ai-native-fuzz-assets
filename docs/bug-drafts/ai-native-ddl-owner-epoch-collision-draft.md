# DDL owner epoch token can collide across rapid owner handoff

## Status

- Current master candidate: `13282a8bd06b`
- Evidence level: root-boundary RED plus local-fix GREEN
- Remote `found_bug`: id1260006, `status=issue-filed,confirmed=0`
- GitHub issue: https://github.com/pingcap/tidb/issues/69755
- User-facing severity still needs live/testbed lift
- Asset result: `/Users/bba/pc/ai-native-assets/issue51846-refill-owner-epoch-results.jsonl`

## User-Visible Shape

During a long DDL reorg, for example `ADD INDEX`, the same TiDB instance may retire from DDL owner and become owner again while an old reorg worker result is still pending. The intended behavior is that a result from the previous owner epoch is rejected and the new owner retries the reorg path.

Current code can accept that stale result if the two owner epochs happen inside the same wall-clock second. The practical symptom would be DDL progress, row count, warnings, or a reorg error from the previous owner epoch being applied in the new owner epoch instead of going through the owner-handoff retry path. A live test should look for skipped `owner ts mismatch` retry around forced owner retire/re-become.

## Source Proof Obligation

```text
P: runReorgJob records beOwnerTS and accepts a reorgFnResult if res.ownerTS == curTS.
Q: ownerTS equality proves the result belongs to the current DDL owner epoch.
F: OnBecomeOwner uses time.Now().Unix(), so two distinct owner epochs can share one token inside a second.
```

Relevant source:

- `/Users/bba/pc/tidb/pkg/ddl/job_scheduler.go`: `OnBecomeOwner`
- `/Users/bba/pc/tidb/pkg/ddl/reorg.go`: `runReorgJob` ownerTS comparison
- `/Users/bba/pc/tidb/pkg/ddl/ddl.go`: `reorgContexts.beOwnerTS`

## RED

Temporary test:

```text
pkg/ddl/reorg_test.go::TestAINativeOwnerEpochSecondCollisionRED
```

Command:

```text
go test ./pkg/ddl -run '^TestAINativeOwnerEpochSecondCollisionRED$' -count=1 -timeout 120s -v
```

Observed:

```text
previousOwnerTS=1000
curOwnerTS=1000
Error: Should not be: 1000
```

Interpretation: a stale result from the previous owner epoch would pass the exact equality predicate used by `runReorgJob`.

RED log:

```text
/Users/bba/pc/ai-native-assets/logs/issue51846-refill-current-owner-epoch-collision-red.log
```

## Local GREEN

Fix shape:

```text
renewOwnerTS(wallTS):
  if wallTS <= previousOwnerTS:
    ownerTS = previousOwnerTS + 1
  else:
    ownerTS = wallTS
```

`OnBecomeOwner` calls `renewOwnerTS(time.Now().Unix())`.

Temporary test:

```text
pkg/ddl/reorg_test.go::TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult
```

Observed:

```text
PASS
ok github.com/pingcap/tidb/pkg/ddl
```

GREEN log:

```text
/Users/bba/pc/ai-native-assets/logs/issue51846-refill-local-owner-epoch-renewal-green.log
```

## Scope Discipline

This is not yet a full cluster reproduction. It proves the root identity predicate is unsound and that a monotonic owner epoch token fixes the predicate. The next validation step is to force owner retire/re-become while an active reorg worker exists, then check whether a late previous-owner result takes the retry path rather than being accepted.

The broad `oracle.allowed-state-after-topology-fault.v1` remains a hypothesis. The validated narrow oracle is:

```text
oracle.ddl-stale-reorg-result-rejected-by-owner-epoch.v1
```

## Method Lesson

Historical issue51846 was about preserving `runningJobs.processingIDs` across owner retire. The reusable selector was not "PD leader partition" itself; it was "owner topology handoff can invalidate a local fact while old async work is still alive."

In current code, that same selector found a different fact:

```text
ownerTS equality proves current owner epoch
```

The small RED/GREEN worked because the broad cluster oracle was reduced to an identity-token proof before running any chaos schedule.

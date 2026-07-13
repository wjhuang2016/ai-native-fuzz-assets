# Distributed ADD INDEX can publish a partial index after a TSO error

Status: issue-filed high severity as remote `found_bug id2040003` and upstream
https://github.com/pingcap/tidb/issues/69789.

## User-visible behavior

A distributed `ALTER TABLE ... ADD INDEX` can finish `synced` and publish the index even though the
planner generated work for only a prefix of the table range. Queries using the index then silently
miss committed rows. The table itself is intact.

On authorized testbed 8220955, job 5456 finished successfully. The table scan returned ids `1` and
`100999`; the forced `idx_v` scan returned only `1`. The equality count for `v=100999` was 1 through
the table and 0 through the index. `ADMIN CHECK TABLE` returned 8223 for handle 100999.

## Trigger

The source range must produce at least two distributed planner batches. A transient TSO allocation
error occurs after one or more batch metas have been appended but before the final batch is planned.
The testbed matrix used two TiDB executors, 101 TiKV regions, local batch size 100, and failed only
the second per-plan `allocNewTS` call.

## Root cause

`generatePlanForPhysicalTable` stores `subTaskMetas` outside a `handle.RunWithRetry` closure. Inside
the region-batch loop it appends metas, but handles a later `allocNewTS` error with:

```go
if err != nil {
    return true, nil
}
```

`RunWithRetry` interprets any nil error as success. The caller checks only that the slice is
nonempty, not that it covers `[startKey,endKey)` exactly once, so a prefix is published as the full
ReadIndex plan.

Changing only the return to `return true, err` is still wrong: the retry appends a complete plan to
the failed attempt's prefix, producing three metas for a two-batch source. The complete fix must
also reset the captured slice or build each attempt in local state and publish only after success.

## Evidence matrix

| Arm | Source batches | Returned metas | Result |
| --- | ---: | ---: | --- |
| Current source | 2 | 1 | RED, nil error |
| Error propagation only | 2 | 3 | RED, retry residue |
| Error propagation + attempt reset | 2 | 2 | GREEN |
| Testbed no fault | 101 regions / 100 per batch | complete | GREEN |
| Testbed second TSO error | 101 regions / 100 per batch | prefix only | RED, DDL synced |

## Reproduction assets

Apply `scaffolds/tidb-tests/ai_native_distributed_backfill_partial_plan_seam.patch`, then place both
`ai_native_distributed_backfill_partial_plan_test.go` and
`ai_native_distributed_backfill_partial_plan_export_test.go` in `pkg/ddl/`. Run the focused test
with `go test -tags=intest ./pkg/ddl -run TestAINativeReadIndexPlanRejectsPartialMetasOnTSOFailure -count=1 -v`.

Current source fails because the fault arm returns one meta with nil error. Changing only
`return true, nil` to `return true, err` still fails because the retry returns three metas. Resetting
the attempt payload in addition to propagating the error makes the same test pass with two metas.

## Fix direction

1. Return the real TSO error so the retry framework can act.
2. Make derived plan state attempt-local, or clear it before every retry attempt.
3. Before publishing, validate exact ordered coverage of the original source range and reject gaps
   or overlaps.

## Method result

This case promotes `RETRY_ATTEMPT_DERIVED_PAYLOAD_ATOMICITY`: every retry attempt must own a fresh
derived payload, and success must prove payload completeness. Error propagation and retryability are
not sufficient when the closure mutates captured state.

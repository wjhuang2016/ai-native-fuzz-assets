# Autocommit retry mixes an old explicit ID with a new row payload

Status: issue-filed high-severity correctness bug on current TiDB and real TiKV.

Upstream: [TiDB #69845](https://github.com/pingcap/tidb/issues/69845).

## Production trigger card

- `production_workload`: a batch entity synchronization or migration job runs autocommit
  `INSERT INTO target(id,payload) SELECT target_id,payload FROM staging ... ON DUPLICATE KEY
  UPDATE`. The target primary key is `AUTO_INCREMENT`, but the supported SQL supplies external
  entity IDs explicitly.
- `natural_producer`: an incremental reconciliation transaction corrects one stable staging slot
  from external ID `100` / payload `old` to ID `200` / payload `new`, and updates another hot target
  entity that the batch also covers. A large scan, expression work, storage latency, or backoff keeps
  the batch's old attempt in flight until the incremental transaction commits.
- `ordering`: batch old snapshot and scan start < incremental staging correction and hot-target
  update < incremental commit < batch prewrite conflict on the hot target < transparent batch retry
  with new snapshot < old positional ID cache consumption < successful commit.
- `defaults`: classic TiDB, MDL ON, autocommit ON, `tidb_retry_limit=10`, and
  `pessimistic-auto-commit=false`. No SQL variable deviation, failpoint, DDL, or node failure.
- `topology`: one TiDB, one TiKV, and two SQL sessions. Every component remains healthy.
- `production_outcome`: the batch returns success with `ExecRetryCount=1`. A fresh session reads
  staging `(target_id=200,payload=new)` but target `(id=100,payload=new)`. `ADMIN CHECK TABLE` is
  green because indexes are structurally consistent with the wrong row identity.
- `control`: no conflict and B-before-A yields `(200,new)` with no retry. The exact owner
  counterfactual retains the same conflict and retry but yields `(200,new)`. A-before-B yields
  `(100,old)`, so `(100,new)` is outside the complete legal single-attempt result set.

## Owner chain

```text
first INSERT SELECT attempt reads explicit IDs [1, 100]
  -> adjustAutoIncrementDatum stores both in RetryInfo.autoIncrementIDs
  -> target row 1 prewrite conflicts
  -> session.retry resets only the positional offset and reruns the statement
  -> retry reads explicit IDs [1, 200] and payload [from-one, new]
  -> adjustAutoIncrementDatum consumes [1, 100] before inspecting current datums
  -> target commits [1/from-one, 100/new]
```

The source owner is not `StmtCtx.InsertID` from #69827. That owner only affects the OK packet.
Here `RetryInfo.autoIncrementIDs` is consumed by the successful retry and changes the actual row key
sent to TiKV.

## Historical distinction

TiDB #20629 and PR #20659 established that retry rowsets may change in size and made the auto-ID
array a refillable buffer. That closes the generated-ID shortage error. It does not validate the
assumption that every cached element is an interchangeable generated allocation: explicit IDs are
stored in the same buffer, and their entity identity may change between attempts. This bug therefore
has a different obligation and consequence: row-identity binding plus silent durable corruption.

## Evidence

- Real-TiKV RED: `runs/autoid-retry-mixed-row-real-tikv-20260714/real-tikv-red.log`
- Exact owner GREEN: `runs/autoid-retry-mixed-row-real-tikv-20260714/real-tikv-owner-green.log`
- Reproducer: `scaffolds/tidb-tests/ai_native_autoid_retry_mixed_row_test.go`
- Counterfactual: `scaffolds/tidb-tests/ai_native_autoid_retry_mixed_row_owner_green.patch`

The RED log records a real `9007`, `retryCnt=0`, `Exec_retry_count: 1`, `Succ: true`, and the
`expected 200/new, actual 100/new` fresh-state mismatch. The GREEN keeps the same retry and passes
the source/target anti-join oracle.

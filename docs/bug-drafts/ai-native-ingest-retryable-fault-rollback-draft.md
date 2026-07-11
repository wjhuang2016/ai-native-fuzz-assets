# Ingest retryable TiKV error family is treated as fatal by ingest-mode DDL

## Status

- Remote `found_bug`: id1320001
- Severity: high
- Status: confirmed
- Root cause id: `ddl-ingest-retryable-kv-family-misclassified-fatal`

## User-visible shape

During ingest-mode online DDL, a single retryable TiKV/ingest leader-change style error can make
the whole DDL fail and roll back immediately.

Confirmed user-visible error shapes:

```text
[Ingest:NotLeader]mock ingest not leader
[Ingest:RegionNotFound]mock ingest region not found
[KV:ErrNoLeader]region has no leader, region '42'
```

Confirmed affected user operations:

```text
ALTER TABLE ... ADD INDEX
ALTER TABLE ... ADD PRIMARY KEY
ALTER TABLE ... MODIFY/CHANGE COLUMN
```

This is high-severity because these are long-running online DDLs on large tables. A brief leader
change or transient region movement can waste the whole reorg instead of being absorbed by retry.

## Strong local evidence

### 1. The classifier gap is selective, not generic

`/Users/bba/pc/ai-native-assets/logs/ddl-retry-classifier-gap-local.log` shows:

```text
ingest_kv_not_leader:      raw=false ddl_synth=false lightning=true
ingest_kv_region_not_found: raw=false ddl_synth=false lightning=true
ingest_kv_no_leader:        raw=false ddl_synth=false lightning=true
grpc_unavailable:           raw=true  ddl_synth=false lightning=true
```

So this is not "DDL cannot retry transient faults". It is a narrower error-domain bridge gap:
the ingest/TiKV retryable family loses retryability at the DDL reorg gate.

### 2. ADD INDEX and MODIFY COLUMN both roll back on the ingest/TiKV retryable family

From `/Users/bba/pc/ai-native-assets/logs/ingest-retryable-family-outcome-probe.log`:

- `ADD INDEX / Ingest:NotLeader`
  - convert to rollback: `1733`
  - `State:rollingback`: `1738`
  - `State:rollback done`: `1746`
- `MODIFY COLUMN / Ingest:NotLeader`
  - convert to rollback: `2668`
  - `State:rollingback`: `2672`
  - `State:rollback done`: `2675`
- `ADD INDEX / Ingest:RegionNotFound`
  - convert to rollback: `3594`
  - `State:rollingback`: `3599`
- `MODIFY COLUMN / Ingest:RegionNotFound`
  - convert to rollback: `4529`
  - `State:rollingback`: `4533`
- `ADD INDEX / KV:ErrNoLeader`
  - convert to rollback: `5456`
  - `State:rollingback`: `5461`
  - `State:rollback done`: `5469`
- `MODIFY COLUMN / KV:ErrNoLeader`
  - convert to rollback: `6393`
  - `State:rollingback`: `6397`
  - `State:rollback done`: `6400`

### 3. ADD PRIMARY KEY shows the same red pattern, while a sibling transient family stays green

From `/Users/bba/pc/ai-native-assets/logs/ingest-add-primary-key-retry-family-local.log`:

- `ADD PRIMARY KEY / grpc_unavailable`
  - outer retry sleep: `1776`
  - terminal `State:synced`: `1861`
  - user-visible `alter err: <nil>`: `1866`
- `ADD PRIMARY KEY / Ingest:NotLeader`
  - convert to rollback: `2827`
  - `State:rollback done`: `2840`
  - user-visible error: `2851`
- `ADD PRIMARY KEY / Ingest:RegionNotFound`
  - convert to rollback: `3805`
  - `State:rollback done`: `3818`
  - user-visible error: `3829`

This is valuable because it proves the same DDL can survive one transient family and still fail
immediately on the ingest/TiKV retryable family.

## Source proof

The mismatch is explicit in source:

- `/Users/bba/pc/tidb/pkg/lightning/common/retry.go`
  - `retryableErrorIDs` includes:
    - `ErrKVNotLeader`
    - `ErrNoLeader`
    - `ErrKVRegionNotFound`
- `/Users/bba/pc/tidb/pkg/ingestor/ingestctrl/job_worker.go`
  - `isRetryableImportTiKVError(err)` delegates to `common.IsRetryableError(err)`
  - ingest worker handles these errors by rescan/retry rather than terminal failure
- `/Users/bba/pc/tidb/pkg/ddl/index.go`
  - `isRetryableError(err, retryUnknown)` only checks DDL reorg retryable messages/codes
  - when the error shows up as foreign `Ingest` / `KV` class, it falls out of the retryable set
  - `runReorgJobAndHandleErr` then converts the job to rollback
- `/Users/bba/pc/tidb/pkg/ddl/job_worker.go`
  - `toTError` synthesizes foreign errors into generic DDL unknown errors for job persistence

The warning signal is visible in logs too:

```text
Unknown error class [class=Ingest]
Unknown error class [class=KV]
```

So the bug is not just "one test failed". It is a concrete domain-bridge mismatch:

```text
inner ingest retry logic: these are retryable
outer DDL reorg classifier: these are fatal
```

## Severity call

This clears the current severe bar better than the earlier modify-column-only lane:

1. it affects core online schema-change operations;
2. it is triggered by realistic transient region/leader churn, not only synthetic socket strings;
3. it wastes user-visible long-running work by rolling back immediately;
4. it spans more than one DDL shape, so it is a shared ingest-mode availability gap.

## Fix direction

Candidate repair directions:

1. for ingest-mode DDL, reuse `lightning/common.IsRetryableError` for the ingest/TiKV error family;
2. extend the DDL reorg retry bridge so `ErrKVNotLeader`, `ErrKVRegionNotFound`, `ErrNoLeader`
   stay retryable instead of falling through as fatal foreign classes;
3. preserve richer retryable/fatal identity across `toTError` and the job retry gate instead of
   collapsing these errors into unknown DDL classes.

## Live-lift path

The current evidence is local failpoint-confirmed. The best live next step is narrower than broad
chaos:

1. active-window ingest DDL on a failpoint-enabled TiDB image; or
2. targeted TiKV leader churn / region-movement fault during active ingest reorg.

The live goal is specifically to force a true `NotLeader` / `RegionNotFound` / `NoLeader`-class
error into the DDL retry bridge, not just to freeze progress.

## Live-lift boundary learned so far

The first live-lift round on testbed `8220955` is now complete, and it is useful evidence even
though it is not a live red hit yet.

Using `/Users/bba/pc/ai-native-probes/add_index_tikv_bounce_oracle_probe.go`, we ran active-window
ingest DDL and only injected TiKV pod bounces after the job was already in `write reorganization`.
The probe had to be hardened first:

1. prefer the real `add index` subjob in `information_schema.ddl_jobs` instead of the parent
   multi-schema wrapper;
2. filter by a pre-submit `job_id` watermark so repeated runs cannot accidentally match old DDL
   history.

Current live results:

1. `single add-index + single TiKV bounce` -> `synced`, final oracle green
2. `multi add-index + single TiKV bounce` -> `done/synced`, final oracle green
3. `multi add-index + sequential dual TiKV bounce` -> `done/synced`, final oracle green

Evidence logs:

- `/Users/bba/pc/ai-native-assets/logs/ingest-live-tikv-bounce-single.log`
- `/Users/bba/pc/ai-native-assets/logs/ingest-live-tikv-bounce-multi.log`
- `/Users/bba/pc/ai-native-assets/logs/ingest-live-tikv-bounce-dual-fixed-watermark.log`

Interpretation:

- this does **not** refute the local retry-classifier bug;
- it does show that coarse pod-level TiKV churn is still too far from the actual classifier input;
- the right status is `LIFT_BLOCKED(fault-shape gap)` / `NEGATIVE_BOUNDARY`, not "bug disproved".

So the next live work should move closer to the error-identity bridge:

1. failpoint-enabled TiDB image;
2. TiDB<->specific-TiKV gRPC drop/blackhole rather than full pod deletion; or
3. controlled leader-transfer / region-movement schedules that can surface true
   `NotLeader` / `RegionNotFound` / `NoLeader` errors without first turning into generic topology
   outage noise.

## Live semantic lift now achieved

We now have a stronger live result on testbed `8220955`, and it sharpens the real fault shape.

### 1. Lower-bridge ingest failpoints are GREEN

On a commit-matched failpoint owner (`fp-tidb`, built from cluster commit `5c9198e948`), these
existing failpoints were exercised live:

1. `github.com/pingcap/tidb/pkg/ingestor/ingestctrl/FailIngestMeta=1*return("notleader")`
2. `github.com/pingcap/tidb/pkg/ingestor/ingestctrl/NoLeader=1*return(true)`

They did hit. `/tmp/fp.log` recorded:

- `meet retryable error when ingesting, will handle the job later` with `[Ingest:NotLeader]`
- `meet retryable error when writing to TiKV` with `[KV:ErrNoLeader]`

But both live `ADD INDEX` jobs still finished `synced`.

This is very important: the inner `ingestctrl` layer can absorb these faults by retry/rescan. So
the bug is not "any live NotLeader breaks ingest DDL".

### 2. Bridge-proximal DDL classifier injection is RED across family members and DDL shapes

To test the actual bug claim, we added one narrow live failpoint in the commit-matched worktree:

```text
github.com/pingcap/tidb/pkg/ddl/mockDDLIngestClassifierErr
```

It injects `ErrNoLeader` / `ErrKVNotLeader` / `ErrKVRegionNotFound` only after
`runReorgJobAndHandleErr(...)` returns and before the outer DDL retry classifier decides whether
the error is retryable.

The live RED matrix now covers more than one family member and more than one DDL shape:

1. `ADD INDEX + noleader`
   - table: `t4`
   - user-visible error: `ERROR 1105 (HY000): region has no leader, region '42'`
   - terminal job: `rollback done`
2. `ADD INDEX + notleader`
   - table: `t8`
   - user-visible error: `ERROR 1105 (HY000): mock ddl ingest classifier not leader`
   - terminal job: `rollback done`
3. `ADD PRIMARY KEY + noleader`
   - table: `tpk1`
   - user-visible error: `ERROR 1105 (HY000): region has no leader, region '42'`
   - terminal job: `rollback done`
4. `ADD PRIMARY KEY + regionnotfound`
   - table: `tpk5`
   - user-visible error: `ERROR 1105 (HY000): mock ddl ingest classifier region not found`
   - terminal job: `rollback done`

Same-environment GREEN controls all passed immediately after clearing the failpoint:

1. `ADD INDEX` controls: `t5`, `t9` -> `synced`
2. `ADD PRIMARY KEY` controls: `tpk2`, `tpk6` -> `synced`

Representative first RED:

```text
mockDDLIngestClassifierErr = 1*return("noleader")
```

the live `ADD INDEX` on `t4` failed with:

```text
ERROR 1105 (HY000): region has no leader, region '42'
```

and `ADMIN SHOW DDL JOBS` showed:

```text
STATE = rollback done
COMMENT = ingest, thread=1, batch_size=32, max_node_count=3
```

The logs simultaneously showed:

- `Unknown error class [class=KV]`
- `run reorg job failed, convert job to rollback`
- `State:rollingback -> rollback done`

The same environment, after clearing the failpoint, immediately passed the green control `t5` and
finished `synced`.

Representative extended RED:

```text
mockDDLIngestClassifierErr = 1*return("notleader")
-> ALTER TABLE t8 ADD INDEX idx_b(b)
-> ERROR 1105 (HY000): mock ddl ingest classifier not leader
-> job 1584 rollback done

mockDDLIngestClassifierErr = 1*return("regionnotfound")
-> ALTER TABLE tpk5 ADD PRIMARY KEY(a)
-> ERROR 1105 (HY000): mock ddl ingest classifier region not found
-> job 1587 rollback done
```

The owner log is consistent across these cells:

- `Unknown error class [class=KV]` or `Unknown error class [class=Ingest]`
- `run reorg job failed, convert job to rollback`
- `State:rollingback -> rollback done`

Self-contained live evidence logs:

- `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix.log`
- `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix-extended.log`

### 3. Method meaning

This is the real live-method breakthrough:

```text
below-bridge retryable fault    -> GREEN (ingestctrl retries internally)
at-bridge ErrNoLeader           -> RED
at-bridge Ingest:NotLeader      -> RED
at-bridge Ingest:RegionNotFound -> RED
```

So the correct live strategy is not "more chaos". It is "inject at the exact semantic altitude
where the proof obligation lives".

## Method lesson

This was a good demonstration of asset reuse:

1. start from the earlier `TRANSIENT_FAULT_RETRY_CLASSIFIER` lane,
2. reuse the same oracle,
3. narrow from coarse transient faults to a specific retryable error domain,
4. then add a same-operation green control (`grpc_unavailable`) to prove the red family is
   selective rather than generic.

# CommitTsExpired retry can cross the cached-table write lease

Upstream issue: [TiDB #69836](https://github.com/pingcap/tidb/issues/69836).

## Impact

A cached-table write can return `COMMIT` success after its WRITE lease has expired. A second TiDB can
already have loaded the pre-commit snapshot into its table cache, so the cluster exposes two values
for the same row:

```text
cached SELECT:   v=0
fresh/NOCACHE:   v=1
```

This is not limited to a transient stale read. A normal statement such as
`INSERT INTO sink SELECT id, v FROM cached_table` can consume the stale cache and durably write
`copied_v=0` into a regular table while the source row is already `v=1`.

The catalog severity is high and the consequence is C3 silent data integrity. Frequency is lower:
the table must explicitly use TiDB table cache, a live primary lock must outlast the fixed five-second
WRITE lease, and the writer must pause while another TiDB reads the table.

## Concrete production trigger

The trigger does not require DDL concurrency, MDL changes, async commit, 1PC, a TiKV crash, or a
fabricated TiKV response. One realistic production schedule is:

1. A frequently read reference table has been enabled with `ALTER TABLE ... CACHE`. TiDB A starts an
   ordinary optimistic transaction that updates the cached table.
2. The write is large enough that its primary lock remains live beyond the cached-table WRITE lease.
   Current client-go uses `6000 * sqrt(write-size-MiB)` milliseconds, capped by the production
   `ManagedLockTTL=20s`. A roughly 4 MiB write therefore receives about 12 seconds, which is longer
   than the fixed five-second cache WRITE lease. Four MiB is a stable example, not an exact boundary;
   the real condition is `primary lock TTL > cache WRITE lease`.
3. TiDB A acquires the WRITE lease, prewrites the transaction, and validates the initial commitTS.
   Immediately afterward, only A loses progress for more than five seconds. Concrete causes include
   a node-specific TiDB-to-TiKV/PD network interruption, a long stop-the-world runtime pause, severe
   CPU starvation, or an OS/container scheduling stall. Its primary lock remains live because its
   TTL is longer than the lease.
4. TiDB B remains healthy. After A's WRITE lease expires, B executes an ordinary `SELECT` and obtains
   a READ lease. While resolving A's live primary lock, TiKV `CheckTxnStatus` pushes the lock's
   `minCommitTS`, allowing B to load the old committed value into its cache.
5. A resumes and sends the original primary Commit. TiKV correctly rejects it with
   `CommitTsExpired` because the attempted commitTS is below the pushed `minCommitTS`.
6. client-go requests a fresh TSO. That replacement is now later than A's expired WRITE lease, but
   current code does not run the cached-table upper-bound checker again. It sends a second Commit and
   reports SQL success.
7. B keeps serving the cache image loaded before the commit. An application that copies or derives
   data from that cached result can persist an incorrect value into an ordinary table.

The deterministic test stops renewal and holds the first Commit RPC only to make steps 3-5 stable.
It does not invent `CheckTxnStatus`, `minCommitTS`, `CommitTsExpired`, the retry, or either read result;
those are produced by the pinned real TiKV and current client-go paths.

Small transactions are an important negative control. Their default primary lock TTL is about three
seconds, so the reader can roll back the lock before the five-second cache lease expires. That path
does not reproduce this bug and is why the production condition must mention transaction size/TTL.

## Source ownership chain

```text
TiDB cached-table commit
  -> acquire/renew WRITE lease
  -> install commitTSCheck(commitTS < lease)

client-go initial 2PC commitTS
  -> run commitTSUpperBoundCheck(initial commitTS)

TiKV reader lock resolution
  -> CheckTxnStatus pushes primary minCommitTS

TiKV primary Commit
  -> reject attempted commitTS < minCommitTS as CommitTsExpired

client-go CommitTsExpired handler
  -> obtain replacement commitTS
  -> update request and retry
  -> missing commitTSUpperBoundCheck(replacement commitTS)
```

The defect is a value-scoped proof being reused as a path-scoped proof. The initial check establishes
`P(commitTS1)`. The retry changes the value to `commitTS2`; it does not establish `P(commitTS2)`.

## Reproduction and oracle

Reusable fixtures:

- `scaffolds/tidb-tests/ai_native_cached_table_commit_ts_retry_test.go`
- `scaffolds/tidb-tests/ai_native_cached_table_commit_ts_retry_harness.patch`
- `scaffolds/client-go-tests/ai_native_commit_ts_upper_bound_retry_test.go`

Run the TiDB probe on current source:

```bash
go test -tags=intest ./pkg/table/tables \
  -run '^TestAINativeCommitTSRetryCannotCrossCachedTableWriteLease$' -count=1 -v
```

The RED oracle requires all of the following, not merely a retry or an internal field mismatch:

```text
real prewrite
+ real reader-driven minCommitTS push
+ first primary Commit returns real CommitTsExpired
+ second Commit reaches TiKV
+ SQL COMMIT success
+ cached source value 0
+ NOCACHE source value 1
+ regular sink value 0
```

The real-TiKV run used TiDB `b8d04e17`, client-go `01bd8f99`, and TiKV `7ecce12e`, with
`tidb_enable_metadata_lock=ON`, ordinary 2PC, and production `ManagedLockTTL=20s`.

## Exact counterfactual

After obtaining a replacement TSO in the `CommitTsExpired` branch, run the existing
`commitTSUpperBoundCheck` before storing the new value or sending another Commit. In the identical
local and real-TiKV schedule:

```text
first CommitTsExpired still occurs
checker call count: 1 -> 2
second Commit RPC:   2 -> 1
SQL COMMIT:          success -> upper-bound error
cached/fresh source: 0/1 -> 0/0
```

This identifies the missing owner check. A production patch should also review every other commitTS
replacement site and preferably centralize installation of a commitTS candidate.

## Dedup boundary

Post-RED search found related history but no exact current issue:

- TiDB #36885 / PR #37020 cover cached-table inconsistent reads and introduced the lease checker.
- client-go PR #564 handles an error around the initial checker; it does not check replacement values.
- client-go PR #1316 checks primary-key identity in the `CommitTsExpired` branch; it does not rerun
  the cached-table upper-bound proof.

These are related invariant history, not the same missing revalidation edge.

Stop after this root. Row sizes, pause causes, cache contents, SQL copy shapes, and timing variants are
blast radius. Reopen only for another value-replacement owner or a materially different proof.

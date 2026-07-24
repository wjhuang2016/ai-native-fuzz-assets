# PiTR can report success after an AUTO_ID rebase failure and later overwrite restored rows

Status: confirmed on current master as `found_bug id3030003`. Severity is high/major. The durable
consequence is silent row loss, while the full production trigger requires PiTR,
`AUTO_ID_CACHE=1`, a final-repair error, and a destructive generated-ID consumer.

## Summary

PiTR replays auto-increment metadata through raw KV writes. For `AUTO_ID_CACHE=1` tables, this does
not update the centralized autoid service's in-memory state. The final restore path therefore reads
the persisted counter and force-rebases every affected table.

That repair is correctness-critical, but `RebaseAutoIncrementIDForSepAutoIncTables` logs each table
error and continues. The public helper always returns `nil`, so BR can print a restore-success
summary even when the only repair for one table failed.

If the application resumes with the stale allocator, a plain INSERT can fail with a duplicate key.
A generated `REPLACE` is destructive: it can reuse an existing restored primary key, return
success, and silently replace the recovered payload.

## Production trigger

A realistic schedule is:

1. A table uses `AUTO_ID_CACHE=1` for MySQL-compatible no-cache allocation semantics.
2. BR restores a snapshot and replays logs that advance the table's persisted IncrementID.
3. During final per-table rebase, one metadata transaction gets a transient TiKV error, or the
   autoid owner changes and the RPC returns `not leader`.
4. BR logs one warning but completes the point restore successfully.
5. Traffic resumes before the autoid service is restarted or otherwise refreshed.
6. An application upsert implemented with `REPLACE` allocates an already-restored ID.

The metadata read uses `kv.RunInNewTxn(..., retryable=false)`. The autoid client retries errors whose
text contains `rpc error`, but a server-side `not leader` response is returned as another error and
is not retried by this call. Either error is swallowed by the outer helper.

## Reproduction

Use the standalone test asset:

```text
scaffolds/tidb-tests/ai_native_pitr_autoid_rebase_failopen_test.go
```

Place it in `br/pkg/restore/log_client`, enable the repository failpoints, and run:

```bash
make failpoint-enable
go test -tags=intest ./br/pkg/restore/log_client \
  -run '^TestAINativePITRRebaseFailureCanOverwriteRestoredRow$' -count=1
make failpoint-disable
```

The test does not modify product code. It uses the existing
`github.com/pingcap/tidb/pkg/kv/mockCommitErrorInNewTxn` failpoint after raw replay state is prepared.

## RED

The injected final-rebase transaction returns `mock commit error`. The restore helper logs:

```text
failed to rebase auto-increment allocator after PiTR log replay
```

but returns success. The next statement:

```sql
REPLACE INTO t(data) VALUES ('replacement');
```

reports `ROW_COUNT()=2` and `LAST_INSERT_ID()=2`. A fresh table scan changes:

```text
2 restored-two
```

to:

```text
2 replacement
```

## Matched GREEN

The test restores the overwritten preimage, disables only the transaction fault, and reruns the same
public rebase helper and SQL statement. The allocator is rebased to `1004000`; the next generated ID
is `1004001`, `ROW_COUNT()=1`, and `id=2/restored-two` remains intact.

## Root cause

The source proves the mismatch directly:

```text
raw replay creates stale autoid state
  -> final per-table rebase is the only repair
  -> repair error is classified "Best effort"
  -> helper returns nil
  -> restore continues to success
  -> no later generated-ID closure check runs
```

The failure is not an optional metric or cleanup error. It leaves the exact state that the rebase
was introduced to prevent.

## Deduplication

Issue `#69485` owns the original unconditional stale-autoid bug. Current master includes its repair.
`id3030003` is the repair's independent fail-open error contract: one transient per-table error
recreates the unsafe state while BR still reports success. Post-RED issue/PR and bug-library searches
found no report for this root.

## Fix direction

Treat any affected-table rebase failure as restore failure. A retry design is also valid if it:

1. retries both metadata reads and autoid owner transitions;
2. records the identity of every table requiring repair;
3. proves every table reached its persisted high water;
4. refuses the success terminal until the set of unresolved repairs is empty.

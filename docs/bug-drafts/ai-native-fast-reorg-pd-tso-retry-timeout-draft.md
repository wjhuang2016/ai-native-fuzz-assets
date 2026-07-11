# Fast reorg ADD INDEX rolls back on transient PD TSO stream retry timeout instead of retrying

## Status

- Remote `found_bug`: id1290001
- Severity: high
- Status: confirmed
- Root cause id: `addindex-fastreorg-pd-tso-retry-misclassified-fatal`
- Testbed: 8220955
- Version rechecked on 2026-07-10:
  `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`

## User-visible shape

During online `ADD INDEX` on the fast-reorg / ingest path, two short PD restarts inside the active
write-reorganization window can make the user DDL fail with:

```text
ERROR 1105 (HY000): create TSO stream failed, retry timeout
```

The cluster becomes healthy again, but the DDL does not recover. The job rolls back immediately.

## Live evidence

- `mysql.tidb_ddl_history` recheck on 2026-07-10:
  - job `1192`: `err.rfccode=PD:client:ErrClientCreateTSOStream`, `err_count=1`, `state=3`
  - job `1204`: `err.rfccode=PD:client:ErrClientCreateTSOStream`, `err_count=1`, `state=3`
- `ADMIN SHOW DDL JOBS`:
  - job `1192`: `rollback done`
  - job `1204`: `rollback done`
- Sibling control:
  - job `1196` with `fast_reorg=OFF` finished `synced` on the `txn` path under the same broad
    PD-bounce lane.

The key point is `err_count=1`: this is not "retried many times and still failed". It is "the
transient fault was classified as fatal on first hit".

## Source proof

```text
P: fast-reorg add-index assumes transient PD TSO failures stay on a retryable recovery path.
Q: routing the PD normalized error through the DDL retry classifier preserves retryability.
F: unknown RFC class PD falls back to a generic SQL code, so isRetryableError returns false.
```

Relevant source anchors:

- `/Users/bba/pc/tidb/pkg/ddl/index.go`
- `/Users/bba/pc/tidb/pkg/util/dbterror/ddl_terror.go`
- `/Users/bba/pc/tidb/pkg/parser/terror/terror.go`
- `/Users/bba/pc/tidb/pkg/ddl/ingest/checkpoint.go`

Local classifier probe:

- `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go`

Current code logs:

```text
Unknown error class [class=PD]
isRetryableError(...)=false
```

for raw/traced/stacked forms of
`PD:client:ErrClientCreateTSOStream(... retry timeout)`.

## Minimal reproduction shape

1. Build a large split table so `ADD INDEX` stays in write reorganization long enough.
2. Run `ADD INDEX` with:
   - `tidb_enable_dist_task=OFF`
   - `tidb_ddl_enable_fast_reorg=ON`
3. Bounce PD twice while the DDL is still live and the schema state is `write reorganization`.
4. Observe the user error above.
5. Read `mysql.tidb_ddl_history` and verify:
   - `err.rfccode='PD:client:ErrClientCreateTSOStream'`
   - `err_count=1`
   - terminal state is rollback.
6. Run the sibling control with `tidb_ddl_enable_fast_reorg=OFF`; it should stay GREEN on the
   `txn` path.

## Fix direction

- Teach DDL reorg retry classification to treat
  `PD:client:ErrClientCreateTSOStream(... retry timeout)` and equivalent transient TSO errors as
  retryable.
- Or, when a foreign RFC error class falls back to the generic SQL code, do not let that erase
  the retryable/fatal distinction before the DDL retry gate.

## Method lesson

The efficient move was not to add more topology chaos. It was:

1. prove the fault really lands in the active window,
2. read `err_count` and terminal state,
3. compare against a sibling GREEN path.

That collapses a noisy infrastructure fault lane into a precise proof obligation about the retry
classifier.

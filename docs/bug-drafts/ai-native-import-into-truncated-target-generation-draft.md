# NextGen IMPORT INTO can report success after writing into a truncated table generation

## Impact

NextGen `IMPORT INTO` can finish successfully while every imported row is invisible from the target
table. If the table is truncated after a detached import is accepted but before a worker executes
it, the task keeps the retired table ID from its submission-time plan and writes the complete input
there. The table currently named in the statement receives none of the imported rows.

The strongest real-TiKV run reported `finished` with row count `2`. The current table contained only
a marker inserted after `TRUNCATE`, while a physical scan found both imported record keys under the
retired table ID. That key range no longer has a live SQL table owner and is subject to DDL
delete-range cleanup.

## Production trigger

The confirmed schedule uses ordinary operational events:

1. A user submits file `IMPORT INTO ... WITH DETACHED` in a NextGen user keyspace.
2. The SYSTEM-keyspace DXF task is queued while the CSE worker is unavailable, scaling out, or
   backpressured.
3. A maintenance or staging workflow executes `TRUNCATE TABLE` before the worker starts.
4. The worker recovers and executes the cached plan.

Metadata locking is enabled. No transaction setting, failpoint, TiDB restart, TiKV failure, or
non-default SQL mode is required. Pausing the worker only makes a normal asynchronous queue delay
deterministic.

The same missing identity check also lets an atomic table-name swap route the import to the
renamed-away object. That row is useful for diagnosis, while `TRUNCATE` is the unambiguous
data-loss witness because it retires the original table generation.

## Reproduction

Use a NextGen real-TiKV/CSE test stack with a SYSTEM keyspace, one user keyspace, object storage, and
a TiKV worker.

1. Stop the TiKV worker.
2. Create an empty `t` and submit:

   ```sql
   IMPORT INTO t
   FROM 'gs://stale-generation-source/data.csv?...'
   WITH DETACHED, cloud_storage_uri='gs://stale-generation-sort/data?...';
   ```

3. After the job and SYSTEM DXF task are visible, execute:

   ```sql
   TRUNCATE TABLE t;
   INSERT INTO t VALUES (100, 'new-generation');
   ```

4. Start the TiKV worker and wait for the import job to finish.
5. Compare the job status, current table ID and rows, and raw record keys under the pre-truncate
   table ID.

The reusable real-TiKV test is
`scaffolds/tidb-tests/ai_native_import_into_truncated_generation_test.go`.

Observed:

```text
job submitted:       id=150001, state=finished, row-count=2
table ID:            44 before TRUNCATE, 46 after TRUNCATE
current t rows:      (100, "new-generation")
retired ID 44 rows:  2 record keys
ADMIN CHECK TABLE t: success
```

## Expected result

An import must not acknowledge rows that have no live target generation. An incompatible DDL should
be blocked while the import owns the table, or the scheduler/worker should reject the task after the
table generation changes.

## Actual result

The task and import job both succeed. All input rows are written under the retired physical table
ID, and the current target table contains none of them.

## Source chain

- `pkg/executor/importer/import.go:540-559` snapshots `TableInfo` and its ID into the import plan.
- `pkg/dxf/importinto/job.go:113-127` creates the import job. Classic mode sets
  `TableModeImport`; NextGen skips that protection.
- `pkg/dxf/importinto/job.go:143-166` crosses from the user keyspace job to a separate SYSTEM
  keyspace DXF task.
- `pkg/dxf/importinto/scheduler.go:315-340` starts preparation without checking that the cached table
  ID is still the current generation of the submitted target.
- `pkg/dxf/importinto/task_executor.go:102-123` reconstructs the table directly from cached
  `Plan.TableInfo`.
- `pkg/dxf/importinto/scheduler.go:870-886` records completion and statistics against that same old
  ID.

## Evidence

- Real CSE/TiKV worker-queue RED:
  `assets/store/logs/import-into-truncated-generation-worker-queue-red-20260725.log`
- Name-swap diagnostic RED:
  `assets/store/logs/import-into-renamed-target-worker-queue-red-20260725.log`
- Scheduler stale-identity unit RED:
  `assets/store/logs/import-into-target-generation-scheduler-unit-red-20260725.log`
- No-DDL real-TiKV control:
  `assets/store/logs/import-into-target-generation-control-realtikv-20260725.log`
- TiDB source: `231dad5225f0d3c9cf38d4ab7ebc03a5326785c7`
- Clean-product-path probe binary:
  `838d3da819617266c2878be105552b11e19b1d6044fd95f69fe38f0014be4b19`

## Severity

Confirmed `high`: the public operation succeeds after losing the full imported dataset from the
live SQL object. The consequence is C3 direct data integrity/data loss. A `critical` label needs an
additional product-scope decision about NextGen exposure, expected import size, and how often
maintenance can overlap worker queue delay.


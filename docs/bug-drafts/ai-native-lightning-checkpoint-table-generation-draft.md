# TiDB Lightning can silently skip a recreated target table

## Impact

Classic TiDB Lightning can exit successfully while importing none of the current nonempty input.
The trigger is a retained completed checkpoint plus a same-name target table that was dropped and
recreated. The new table is a different TiDB generation, but the old completed status and engines
are accepted as its progress.

## Reproduction

1. Configure a file checkpoint with `keep-after-success = "origin"` and TiDB backend.
2. Import a CSV containing rows `(1,'old-generation')` and `(2,'old-generation')` into `db.t`.
3. Record the target `TIDB_TABLE_ID`, then `DROP TABLE db.t` and recreate the same schema.
4. Confirm the new table ID differs and the table is empty.
5. Run the same Lightning command with the retained checkpoint.
6. Observe the process exit status and the current table rowset.

On testbed 8220955, the first table was ID 5412 with two rows. The recreated table was ID 5415.
The second Lightning run exited 0 but ID 5415 remained empty. `ADMIN CHECK TABLE` stayed green
because an empty table is internally consistent; it cannot detect skipped source input.

## Root Cause

Checkpoint table state is keyed by table name without a real target-generation binding.
`FileCheckpointsDB.Initialize` preserves existing status, engines, and chunks and explicitly leaves
hash validation as a TODO. The MySQL checkpoint path writes constant value 30 into its hash field.
For TiDB backend, the persisted TableID is 0, so a simple ID comparison cannot close the gap.

## Fix Direction

Persist a real target-generation fingerprint after schema creation and validate it before any
status, engine, chunk, checksum, or postprocess state is consumed. Mismatch must reject or reset the
table checkpoint. The fingerprint must not collapse to table name, constant hash, or zero TableID.

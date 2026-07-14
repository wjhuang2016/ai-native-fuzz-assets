# Failed server-info restart can let MDL DDL corrupt a secondary index

Status: confirmed high-severity correctness bug on current TiDB and real TiKV. Not filed upstream yet.

## Production trigger card

- `supported_workload`: TiDB A holds an ordinary explicit pessimistic transaction that has inserted
  a row. TiDB B runs `ALTER TABLE ... ADD INDEX` on the same table. This is a supported online-DDL
  and transaction combination.
- `natural_producer`: only A's server-info etcd session ends. During recovery, the replacement lease
  grant succeeds, but a short etcd leader/recovery flap makes all five server-info `Put` attempts
  fail. The replacement lease remains healthy after the flap, so its `Done` channel never asks the
  loop to retry publication.
- `ordering`: old transaction insert < server-info session end < replacement session grant < failed
  server-info publication < TiDB B membership read < ADD INDEX completion < old transaction commit.
- `lifetime_inequalities`: A's schema-sync session must remain live; the replacement server-info
  session must outlive ADD INDEX plus COMMIT; the old transaction connection and pessimistic lock
  heartbeat must remain live.
- `defaults`: classic TiDB, MDL ON, server-info session TTL 90 seconds, five key-operation retries,
  and ordinary pessimistic transaction defaults. No SQL variable change is required.
- `topology`: two TiDB frontends and normal PD/TiKV. The compressed real-TiKV test uses one domain
  because the DDL owner consumes the same etcd membership contract.
- `public_and_durable_result`: ADD INDEX returns success and COMMIT returns success. A fresh table
  scan returns `(1,10)`, a forced new-index scan returns no rows, and `ADMIN CHECK TABLE` returns
  error 8223.
- `control`: a whole-process 95-second stall is not a trigger. It also restarts schema sync, advances
  the validator restart version, and makes COMMIT return 8028. The exact owner counterfactual
  republishes membership, makes DDL wait for the old transaction, and finishes with one table row,
  one index row, and green `ADMIN CHECK`.

The trigger is narrow but production-shaped. It does not require disabling MDL. It does require one
etcd lease to fail independently of the schema-sync lease, so frequency should not be overstated.

## Proof obligation

```text
P: NewSession returned a live replacement server-info session.
Q: assigning that session to s.session is safe before the server-info key is durably published.
F: StoreServerInfo can fail; the loop then waits on the live unpublished session and suppresses retry.
```

`Q` is false. Session liveness and membership publication are different facts.

The consequence crosses a second proof boundary. MDL transactions set
`needCheckSchemaByDelta=false` because DDL is expected to wait for every live TiDB. The DDL owner
constructs that wait set from `/tidb/server/info`. Once A is missing from the set, DDL finishes and
A's schema checker trusts a wait that never covered it.

## Real-TiKV evidence

Current source:

```text
server info syncer need to restart
server info syncer restart failed: mock store server info error
COMMIT: success
tableRows: [[1 10]]
indexRows: []
ADMIN CHECK: 8223 data inconsistency
```

Exact owner counterfactual:

```text
server info syncer restart failed: mock store server info error
server info syncer need to restart
server info syncer restarted
DDL remains blocked until old COMMIT
tableRows: [[1 10]]
indexRows: [[1 10]]
ADMIN CHECK: green
```

The fault is accepted only when the same run logs the failing `StoreServerInfo` stack. Earlier runs
that enabled a marker without failpoint source conversion are invalid and are recorded as such.

## Fix direction

Treat the replacement session and its registration key as one publication unit. Do not expose the
replacement through `s.session` until `StoreServerInfo` succeeds. On error, close the unpublished
replacement and leave a completed owner that causes another loop iteration, or make restart retry
registration explicitly with bounded backoff and shutdown cancellation.

The fix must also avoid a leaked replacement session and should use synchronized session ownership;
the current field is read by the sync loop and min-start-TS reporter.

## Dedup boundary

Post-RED searches for `server info syncer`, `StoreServerInfo restart`, and MDL/add-index
inconsistency found no exact TiDB issue. Whole-node PD outage reports are not duplicates unless they
show the same live-unpublished replacement owner and both-success row/index divergence.

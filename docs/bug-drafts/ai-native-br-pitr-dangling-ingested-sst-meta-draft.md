# BR retry can leave a missing metadata reference that blocks later PiTR

Status: confirmed on current TiDB master by a persistent-storage RED, a successful restore retry,
the real log-restore metadata consumer, and an exact ordering counterfactual. No exact upstream
issue or PR was found in the post-RED duplicate search.

## Summary

When log backup is enabled, BR snapshot restore copies every ingested SST into the log-backup
storage. Before copying the first batch, it records an `IngestedSstPaths` entry in a durable
migration and then creates the referenced `extbackupmeta` object.

If the BR process exits between those two writes, the migration remains while the referenced object
does not exist. A retry generates a new directory name and publishes a second, valid reference. It
does not repair the first one.

Later PiTR enumerates every historical `IngestedSstPaths` entry and fails on the missing object. A
snapshot restore that eventually succeeded can therefore leave the log-backup chain unusable.

## Production trigger

The production shape is:

1. a log backup task is running;
2. an operator performs a BR full or table snapshot restore into that cluster;
3. the BR process or Pod is killed, crashes, is evicted, or loses the node after the migration write
   reaches object storage and before the initial `extbackupmeta` write reaches object storage;
4. the operator retries the restore and it succeeds;
5. a later point-in-time restore reads the same log-backup storage.

Normal returned errors are less dangerous because BR's close callback tries to persist unfinished
metadata. The confirmed trigger requires an abrupt process lifetime boundary. It needs no SQL race,
multiple TiDB nodes, disabled MDL, or unusual session variables.

The timing window is short, but the resulting state is durable. A primary-cluster loss can expose
the failure much later, when the poisoned log-backup chain is the recovery source.

## Environment

```text
TiDB master: 05b396fb6636f73b3bc06b09107cf43f2c725c35
Storage: real local external-storage implementation
Consumer: stream.LoadIngestedSSTs, used by log_client.WithMigrations.IngestedSSTs
MDL: unrelated; default enabled
```

## Reproduction

The focused test uses the production storage interfaces and sequence:

1. fail only the first collector's `extbackupmeta` write;
2. keep the already appended migration and omit graceful collector close, modeling process exit;
3. reopen the restore with a new generated directory;
4. restore one SST, mark the retry successful, and close normally;
5. load all migration paths through `stream.LoadIngestedSSTs`.

Current master produces:

```text
migration paths after retry:
  v1/ext_backups/.../extbackupmeta
  v1/ext_backups/...-1/extbackupmeta

stale meta read error:
  no such file or directory

log restore ingested-SST error:
  failed to read backup at v1/ext_backups/.../extbackupmeta
```

The normative assertion requires the post-retry consumer to succeed and exactly one published path
to exist. It fails on current master.

## Root cause

`pitrCollector.prepareMig` publishes the reference before its target:

```text
AppendMigration(IngestedSstPaths = metaPath)
  -> reset in-memory metadata
  -> WriteFile(metaPath)
```

The reference and target have different durable owners. Process exit can expose the intermediate
state to future consumers.

Retry does not close the gap:

```text
newPiTRColl
  -> fetch a new TSO
  -> name = backup-%016X
  -> metaPath changes
```

The stale migration stays reachable. `WithMigrations.Build` collects every path, and
`LoadIngestedSSTs` returns the first read error instead of ignoring or repairing a missing object.

## Counterfactual

A temporary current-master change performed:

```text
reset metadata
  -> persist initial extbackupmeta
  -> append migration reference
```

The same test then had one valid path after retry and the real log-restore metadata consumer returned
no error. If migration append fails after the reordered write, only an unreachable metadata object
remains; no consumer-visible dangling reference is published.

The temporary product and test changes were removed after evidence capture.

## Expected behavior

A durable migration must not reference an object that has not been durably created. A successful
retry must leave every historical consumer-visible reference readable, or explicitly retire the
failed attempt's reference.

## Impact and severity

This is a disaster-recovery integrity failure. It can make later PiTR fail even though the retried
snapshot restore succeeded. The consequence can become critical when the original cluster is lost
and the log-backup chain is the only recovery source.

Current triage is high with critical impact. The abrupt-exit window is narrower than ordinary
configuration-only triggers, so it should not displace candidates with equally strong consequences
and more common reachability.

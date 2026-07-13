# id2160003: retry receiver state must roll back with the transaction

Remote bug DB: `found_bug id2160003`, confirmed moderate severity.

## Independent discovery

The expanded S45 scanner started from current retry callbacks, not PR findings or historical bugs.
Its first version only reported `e.fetchIndex()` as a captured receiver call. A typed one-level
effect summary then expanded that call into writes to `idxValsBufs`, `idxValues`, `scanRowCnt`, and
`lastIdxKey`; sibling calls also append `batchKeys` and increment `removeCnt`.

`cleanTableIndex` runs those calls inside retryable `kv.RunInNewTxn`. A Commit error rolls back the
index deletes, but none of the executor fields. The next attempt therefore starts from a frontier
and counters that describe work which never committed.

No PR, issue, fix, or history was used before RED. Post-RED searches for the operation, panic, and
function/retry pair found no exact upstream match.

## P / Q / F

- **P:** `RunInNewTxn` rolled back the failed KV transaction and opened a new one.
- **Q:** the cleanup batch can be replayed from the same committed state.
- **F:** the callback reuses receiver fields as if they were transaction-owned.
- **Missing dimension:** attempt-local executor state and its committed publication frontier.

## Minimal matrix

| Cell | Dangling entries | Retry witness | Result |
| --- | ---: | --- | --- |
| invalid harness | 20001 | absent | PASS, rejected because failpoint conversion was off |
| current small | 3 | present | reports 9 instead of 3 |
| current boundary | 20001 | present | panic at `idxValsBufs[20000]` |
| receiver reset | 3 / 20001 | present | exact counts, no panic, index check passes |

The small and boundary cells show one root at two consequences. This is not severe durable
corruption: the command either repairs the index with a wrong count or fails and can be retried.

## Method improvement

S45 source generation now needs a typed callee-effect pass. Direct closure assignments are not
enough because the important mutations may be hidden behind `e.fetchIndex()`. For each retry
callback:

1. bind a direct captured receiver to its concrete method receiver type;
2. summarize writes, increments, appends, and mutable field calls in that method;
3. prove a retryable error or Commit edge exists after the mutation;
4. look for attempt-entry reset or authoritative overwrite before execution;
5. require an explicit edge witness in the oracle output.

The edge-witness rule came from an INVALID run: enabling the runtime failpoint expression without
running TiDB's source conversion did not execute the retry path. A fault configuration is not proof
that the fault landed.

## Negative calibration

`Deleter.gatherKeysToDelete` also mutates receiver buffers inside a retry callback, but its only
fallible operation, `BatchGet`, occurs before those mutations. After the append it returns nil, so
there is no post-mutation retry edge. Receiver mutation alone must not admit a target.

## Stop rule

Count one moderate root for `CleanupIndexExec` batch state. Entry counts, index types, and retryable
error strings are blast radius. Continue S45 only for a different retry owner and state owner whose
survivor changes durable data, terminal error semantics, or control-plane publication.

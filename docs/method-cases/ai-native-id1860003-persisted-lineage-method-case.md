# id1860003: persisted state must bind its semantic lineage

Remote `found_bug`: `id1860003`, confirmed high.

## P / Q / F

- P: a persisted checkpoint authorizes skipping every object at or below its progress token.
- Q: loading the expected state-file path is treated as proof that the token belongs to the current
  upstream task and object lineage.
- F: the file records progress but no producer identity, task generation, cluster identity, or
  storage identity, so a reused bucket can satisfy the path check with semantically foreign state.

## Why the selector worked

The source scan first looked for explicit disaster consequences, then followed the checkpoint from
producer to its highest consumer. The calculator mismatch alone was suspicious; the restore reader
made it severe by preferring the stale value as `logMaxTS` and default restore target.

The decisive matrix changed lineage while keeping the weak selector inputs constant: same task name
and same state path. Same-lineage and no-state controls proved that ordinary resume and first start
were not the problem.

## Method improvement

Add `PERSISTED_STATE_MUST_BIND_LINEAGE` to the LOOP. For every resume token, cache entry, checkpoint,
or manifest that can skip work:

1. List the semantic lineage dimensions: producer, cluster, task generation, source, destination,
   schema/version, and keyspace where relevant.
2. Compare those dimensions with fields actually persisted and validated.
3. Build a two-lineage matrix that keeps names/paths equal but changes one owner identity.
4. Observe both the fast-path decision and the highest consumer that trusts it.
5. Include same-lineage and no-state controls.

Versioning the state format is not enough. A version proves how bytes are decoded, not whose facts
they describe.

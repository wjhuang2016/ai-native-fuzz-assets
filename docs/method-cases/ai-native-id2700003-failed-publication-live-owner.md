# id2700003: publication must precede replacement-owner liveness

Remote `found_bug id2700003`, confirmed high severity / critical consequence.

## Starting proof obligation

The server-info restart path creates a new etcd session, assigns it as the current owner, then
publishes the membership key:

```text
P: replacement session is live.
Q: the restart is represented well enough for the loop to wait on replacement.Done().
F: membership Put fails, so no consumer can observe the replacement.
```

This is a two-resource publication protocol. A live retry owner is not evidence that its payload was
published. Installing the owner first can suppress the only retry edge.

## Small matrix

| Schedule | Fault activated | Schema sync | Membership | COMMIT | Fresh row/index |
| --- | --- | --- | --- | --- | --- |
| direct manager removal | n/a | live | omitted | success | 1/0, INVALID reachability |
| whole TiDB stall >90s | natural | restarted | restored | 8028 | 0/0 |
| server-info close + failed Put | yes | live | omitted | success | **1/0, 8223** |
| same fault + owner restore | yes | live | republished | success after DDL wait | 1/1, green |

The first row found the semantic consequence but was too strong. The second row falsified the broad
production story. The third row compressed the exact independent lease schedule and produced the
admissible RED.

## Selector extension

Extend `FAILED_PUBLICATION_RETAINS_RETRY_OWNERSHIP` with a dual form:

```text
candidate = replacement retry/liveness owner installed before durable publication
            intersect publication error
            intersect loop waits only for replacement-owner completion
            intersect fresh consumer trusts missing shared state
            minus explicit retry, rollback to prior owner, or fail-closed admission
```

Useful source shapes include:

1. `newOwner := create(); current = newOwner; err = publish(newOwner)`;
2. error logging followed by a loop whose next wakeup is `<-current.Done()`;
3. a long-lived replacement lease, watcher, timer, or cursor with no published payload;
4. a downstream quorum, membership, schema, routing, or recovery consumer that treats absence as
   permission rather than unknown state.

## Oracle gate

1. Prove the fault executed in the same run. A configured failpoint is not evidence.
2. Fail exactly one publication and then make the publisher healthy.
3. Prove the replacement owner stays live while shared state is absent.
4. Observe a fresh highest consumer of the absence.
5. Pair public terminal results with durable state.
6. Restore only retry ownership/publication order and rerun the same schedule.
7. Run the broad natural fault as a control; if a sibling owner restarts and fails closed, state the
   narrower independence condition explicitly.

## Method improvement

Add `fault_activation_witness` to every injected matrix row. It can be an exact log stack, call
counter, request capture, or state transition, but it must identify the intended producer. This run
initially produced plausible output while custom `failpoint.Inject` calls were compile-time no-ops.
The rows were invalid until repository failpoint conversion was enabled and the restart failure was
visible in the same output as the corruption.

Stop after one publication/owner/highest-consumer tuple. DDL types, table shapes, transaction modes,
and retry error strings are blast radius.

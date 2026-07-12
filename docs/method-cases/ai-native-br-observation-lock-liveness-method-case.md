# Method Case: an observer must not suppress its own liveness signal

## Starting point

This candidate came from current source only. No PR, review finding, issue, or fix branch was used
to select it.

```text
P: lock the target row so the abort decision is stable
Q: observe heartbeat changes to distinguish live from stale
F: the heartbeat writer needs the target row locked by the observer
```

The missing proof dimension is **measurement independence**. A liveness observation is valid only
if the observer does not prevent the signal producer from making progress.

## Selector

`OBSERVATION_LOCK_SUPPRESSES_LIVENESS_SIGNAL`

Apply it when code:

1. acquires a lock, lease, transaction, mutex, ownership token, or resource reservation;
2. waits for another actor's heartbeat, progress counter, checkpoint, epoch, or terminal result;
3. uses an unchanged signal to infer death, staleness, or safe takeover;
4. performs an irreversible transition such as delete, cleanup, reassignment, or publication.

Build a small interference graph before testing:

```text
observer-held resources -> signal-writer required resources
signal writer           -> liveness/progress field
liveness/progress field -> irreversible observer decision
```

Any path from the observer's held resource to the signal writer is a proof obligation. A direct
cycle means the observer can manufacture the evidence used by its own decision.

## Minimal matrix

| Phase | Owner | Observer-held row lock | Heartbeat | Terminal state |
|---|---|---:|---|---|
| pre-lock altitude | live | no | advances | retained |
| target | live | yes | write conflict / unchanged | deleted (RED) |
| stale control | absent | normal abort | unchanged | deleted (GREEN) |

The failpoints only make phase boundaries deterministic and compress five minutes to two seconds.
The lock conflict and registry deletion are produced by real TiKV and the production decision path.

## LOOP improvement

Extend proof-obligation generation with an interference pass:

```text
find P -> infer Q -> locate irreversible action
  -> name the signal producer
  -> inventory observer-held resources
  -> inventory signal-writer read/write/lock set
  -> detect dominance or cycles
  -> run before-lock / after-lock altitude differential
  -> require a terminal state oracle and a truly stale control
```

This is stronger than searching for missing error checks. The code can check every return value and
still be wrong because the observation protocol changed the behavior being observed.

## Why this worked

The source made a locally reasonable claim: `FOR UPDATE` protects deletion. The AI did not attack
the lock itself; it asked which actor must still make progress while that lock is held. That changed
the candidate generator from keyword matching into ownership reasoning and produced a compact,
high-signal schedule immediately.

## Stop rule

After one live-owner RED, one pre-lock progress proof, and one truly stale GREEN, stop enumerating
heartbeat intervals, task statuses, or restore configurations. Reopen only for fix validation or a
different observer/signal resource cycle.

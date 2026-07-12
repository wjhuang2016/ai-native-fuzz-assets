# Method Case: external effects must follow the durable commit owner

## Starting point

The candidate came from current source ownership and transaction ordering. No PR or review finding
was used.

```text
P: local transaction stages state and remains cancellable
Q: cancellation means the operation had no externally visible effect
F: an external owner commits first and is absent from rollback
```

## Selector

`EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE`

Apply it to DDL, restore, backup, scheduler, privilege, placement, or resource-control paths that:

1. mutate local transactional metadata;
2. call an external service or non-transactional side store;
3. can still fail, conflict, cancel, lose ownership, or restart afterward;
4. publish success/failure through the local transaction owner.

Build this ledger before testing:

```text
local owner:    staged state -> durable commit -> rollback/cancel
external owner: irreversible call -> visible runtime state -> compensation/reconcile
```

Any external call before the local durable boundary requires a compensation or recovery edge.

## Minimal matrix

| Schedule | DDL result | Metadata owner | Runtime owner | Verdict |
|---|---|---|---|---|
| cancel after external mutation | cancelled | old 1000/LOW | new 1/HIGH | RED |
| normal ALTER | success | new 2000/HIGH | new 2000/HIGH | GREEN |
| no precommit external effect | cancelled | old | old | counterfactual |

## LOOP improvement

After P/Q/F and before fault injection:

```text
enumerate owners
  -> mark each durable boundary
  -> order external calls against local commit
  -> enumerate every post-call abort edge
  -> require compensation/reconciliation reachability
  -> pause after external success
  -> trigger a supported cancel/conflict
  -> compare metadata, runtime, history, and user terminal result
```

The important mutation is not an injected error. Both the external call and cancellation succeed.
The bug appears because two successful subsystems disagree on who owns commit.

## Stop rule

One real external-owner RED, one normal publication GREEN, and one counterfactual ownership proof are
enough. Do not enumerate RU values, priorities, or runaway policies under the same ordering root.

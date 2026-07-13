# id2100003: attempt-scoped side-effect rollback closure

Remote bug DB: `found_bug id2100003`, issue-filed high severity; upstream #69791.

## P / Q / fast path

- P: pessimistic retry rolls back the transaction statement buffer, resets statement context, and
  rebuilds the executor.
- Q: every failed-attempt state value that the rebuilt executor can read has returned to the
  statement-entry state.
- Fast path: accept a post-execution lock error as retryable and transparently execute the DML again.

The source check proves only transaction-local rollback. It does not prove closure over session
side effects performed while evaluating the failed row image.

## How current-source analysis found it

The search started at retry acceptance, not at known findings. For each retry boundary it inventoried:

1. state mutated before the error can occur;
2. which rollback owner restores each state dimension;
3. which values the rebuilt executor can consume;
4. the highest user-visible observer if one dimension survives.

`builtinSetRealVarSig.evalReal` directly mutates `UserVars`. In pessimistic DML, `handleNoDelayExecutor`
runs before `KeysNeedToLock` and `LockKeys`; a retryable error can therefore occur after SETVAR. The
accepted retry calls `StmtRollback`, whose concrete implementation cleans only transaction state.
That gives the exact missing edge in the rollback-closure graph.

No PR, issue, fix, or historical finding was used to generate or rank this candidate.

## Why the matrix was efficient

The useful dimension was error altitude relative to side-effect evaluation, not a larger SQL corpus:

```text
no conflict                 -> 1/1 GREEN
conflict before SETVAR      -> 1/1 GREEN
conflict after SETVAR       -> 2/2 RED
same late conflict + := 7   -> 7/7 GREEN
late natural unique race    -> false success + wrong row image RED
restore attempt user vars   -> all cells GREEN
```

The pre/post pair proves causality. The idempotent arm shows that retry itself is not invalid; replay
becomes incorrect when an attempt-local side effect changes a later attempt's input. The unique-key
race raises the oracle from internal state to error semantics and persistent data.

## Reusable selector

`ATTEMPT_SCOPED_SIDE_EFFECT_ROLLBACK_CLOSURE` applies when code automatically retries an operation
after partially evaluating it. Build a mutation/rollback/consumer graph around the retry point and
rank missing rollback edges whose surviving state controls a key, predicate, row image, external
effect, or terminal error.

Required matrix:

- no-retry control;
- error before the candidate mutation;
- the same error after the mutation;
- an idempotent mutation control;
- an exact restore-or-decline-retry counterfactual;
- a highest-consumer oracle such as error identity plus committed row image.

## Method improvement

Fault injection should first establish the altitude differential, then be replaced or lifted by a
natural owner interaction. Here the deterministic breakpoint exposed the post-evaluation boundary;
a real concurrent unique-key insert produced the actual write conflict; finally `SLEEP` inside the
expression converted the schedule into a SQL-only testbed reproduction.

The generator should therefore index retry sites by two machine-extractable sets: side-effecting
callees reachable before the retryable error and rollback callees executed before re-entry. The set
difference supplies candidates. Existing assets then provide the matrix and oracle; history remains
a post-RED dedup channel.

## Stop rule

Count one root for the pessimistic DML retry owner and missing user-variable rollback. Different DML
forms, variable types, expressions, indexes, conflict keys, and retry counts are blast radius. Reopen
only for a different retry owner, a different unrestored state owner, or a stronger independent
terminal consumer.

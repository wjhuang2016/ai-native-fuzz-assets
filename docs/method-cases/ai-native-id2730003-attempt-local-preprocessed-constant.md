# id2730003: preprocessed data constants must be scoped to the retry attempt

Status: confirmed high-severity silent data-integrity root. MDL is ON; pessimistic mode and retry
limits stay at defaults; READ COMMITTED is a common isolation setting.

## Proof obligation

```text
P: the non-correlated scalar subquery was evaluated successfully during planning.
Q: its expression.Constant may remain in ExecStmt.Plan for the whole SQL statement.
F: pessimistic RC retry creates a new statement-TS generation but rebuilds only the executor.
```

The missing arguments are visible when the constant is written as a certificate:

```text
bad:  Constant(value=30)
good: Constant(value=30, statement_ts=t1, attempt_generation=g1)
```

The retry accepts a new statement TS `t2` while consuming the `t1/g1` constant.

## Minimal matrix

| Isolation / schedule | Retry | Scalar / joined source | Verdict |
| --- | ---: | --- | --- |
| RC, publisher after statement TS | 0 | old / old | GREEN, real TiKV |
| RC, publisher before statement TS | 0 | new / new | GREEN control |
| RC, publisher between preprocessing and lock conflict | 1 | old / new | RED, real TiKV |
| Same RC retry, rebuild plan for the new attempt | 1 | new / new | GREEN, local counterfactual |
| RR, old transaction snapshot plus current DML read | 0 | old / new | legal negative calibration |

The RR row was an earlier rejected candidate. Reusing that negative asset exposed the decisive
dimension: under RC, `getStmtReadTSFunc` and `getStmtForUpdateTSFunc` both use one attempt-local
`getStmtTS`. Therefore old/new is outside the complete legal one-attempt result set.

## Selector

Store this as `ATTEMPT_LOCAL_PREPROCESSED_CONSTANT_REUSE`:

```text
candidate = planning/rewrite executes a data read and embeds its result
            intersect transparent retry refreshes read or statement generation
            intersect retry reuses plan or preprocessed expression
            intersect another executor read observes the new generation
            intersect irreversible row/key/error consumer
            minus replan / generation check / fail-closed behavior
```

Inventory calls such as `EvalSubqueryFirstRow`, metadata lookup, privilege/policy resolution, or
dynamic default evaluation that produce ordinary constants or plan fields. Do not start by fuzzing
SQL syntax. Start from the retained data-bearing plan object and ask which retry owner invalidates
its input generation.

## Production shape

A batch allocator updates account rows by joining a routing/configuration table while storing a
global balance or ledger aggregate from a scalar subquery. A concurrent allocator claims the old
unique route, advances the next route, and adds a new account or ledger value. Large scans, cold or
hot Regions, backoff, expression work, or storage pressure keep the first batch attempt alive until
that commit. The old route conflict naturally triggers TiDB's supported pessimistic retry.

The batch returns success and commits a new route with an old aggregate. Structural checks remain
green because row and indexes consistently encode the wrong business value. `SLEEP` is only the
deterministic schedule compressor used by the small probe.

## Root distinction

TiDB #69826 retains a completed materialized CTE through `StmtCtx.CTEStorageMap` and `sync.Once`.
This root has no CTE materialization: the expression rewriter executes a normal scalar subquery and
stores the datum directly in `ExecStmt.Plan` as `expression.Constant{SubqueryRefID: ...}`. Closing
CTE storage cannot repair it; rebuilding the plan does.

## Method lesson

Allowed-outcome negatives are reusable assets, not dead ends. The earlier RR rejection prevented a
false bug claim. Changing only the isolation owner to RC transformed the same symptom into a strong
RED because RC closes the one-attempt set to old/old or new/new. Future retry mining should vary
ownership semantics before varying SQL surface syntax.

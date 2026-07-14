# Pessimistic retry plus scalar subquery: reject a same-final-state false positive

## Initial candidate

A pessimistic `UPDATE` joined a routing source directly and also copied one field through an
uncorrelated scalar subquery:

```sql
BEGIN PESSIMISTIC;
UPDATE account AS a JOIN route_source AS s ON s.tenant_id = 1
SET a.route_id = s.next_route_id,
    a.policy_version = (
      SELECT policy_version FROM route_source WHERE tenant_id = 1
    )
WHERE a.tenant_id = 1;
COMMIT;
```

The source initially contained `(next_route_id=1, policy_version=10)`. A concurrent routing
publisher atomically changed it to `(2,20)` and assigned the released unique route ID `1` to another
account. If the publisher commits after the batch has read the old route ID but before final
`LockKeys`, the first attempt conflicts on route ID `1`. TiDB transparently retries, returns UPDATE
success, and the application's ordinary transaction flow reaches `COMMIT`.

The resulting row was `(route_id=2, policy_version=10)`. A new transaction executing the statement
from the final database state produced `(2,20)`, so this initially looked like a mixed-generation
durable write.

## Seven-field production card

- `production_workload`: a supported explicit pessimistic RR transaction performs a routing,
  account, or tenant migration using one direct source JOIN and one uncorrelated scalar subquery.
- `natural_producer`: an ordinary configuration publisher changes the source pair and reallocates
  the old unique route ID in one transaction. A large batch, expression work, a hot row, or routine
  storage latency widens the interval before final locking.
- `ordering`: scalar snapshot value `10` and first-attempt source value `1` are read before the
  publisher commits; the publisher commits source `(2,20)` plus unique route `1` before first-attempt
  `LockKeys`; the retry then reads current route `2`.
- `defaults`: MDL was ON, isolation was `REPEATABLE-READ`, and pessimistic fair locking was ON in the
  current runtime. `BEGIN PESSIMISTIC` was the only disclosed workload choice.
- `topology`: one TiDB, one real TiKV, and two SQL sessions. No DDL, failover, network fault, async
  commit, 1PC requirement, or user SAVEPOINT was involved.
- `production_outcome`: TiDB hides the `9007` conflict, UPDATE returns success, the client naturally
  commits, and a fresh session reads `(2,10)`.
- `control`: establish the RR snapshot before the publisher commit, then execute the same UPDATE once
  after that commit without any lock conflict or retry.

The SQL-only reproducer used `s.next_route_id + SLEEP(2) * 0` only to hold the already named interval
after the old direct value was read and before final locking. The real-TiKV slow log recorded
`Exec_retry_count=1`, `Exec_retry_time` around two seconds, `Succ=true`, and `IsExplicitTxn=true`.

## The decisive control

The control invalidated the oracle. In a pessimistic RR transaction:

1. a plain `SELECT` established the old transaction snapshot containing policy `10`;
2. the publisher committed source `(2,20)` and assigned unique route `1` elsewhere;
3. the original transaction executed the same UPDATE exactly once.

There was no retry (`ExecRetryCount=0`), but the durable row was still `(2,10)`. The outer DML source
is a current/locking read, while the scalar subquery is a consistent read from the established RR
snapshot. Therefore `(2,10)` is already in the set of legal one-attempt outcomes.

## Matrix

| Cell | Retry count | Durable row | Classification |
| --- | ---: | --- | --- |
| hidden write-conflict retry | 1 | `(2,10)` | candidate signal only |
| new transaction from final state | 0 | `(2,20)` | insufficient control |
| established RR snapshot, one attempt after publisher | 0 | `(2,10)` | contract witness; invalidates candidate |
| decline retry for registered scalar subquery | 0, error 9007 | original target row preserved | conservative behavior, not a bug proof |
| force plan rebuild on retry | 1 | `(2,10)` | expected: rebuild still reads RR snapshot |

All product-level cells used real TiKV `7ecce12e7573f7d4a392877b994fa6af80606369`; the target and
control tables passed `ADMIN CHECK TABLE`.

## Classification

`INVALID(oracle-too-strong)`. Do not insert a `found_bug` row and do not file an upstream issue.
The output differs from a same-final-state new transaction, but it does not violate the supported RR
visibility contract and is reachable without transparent retry.

## Method improvement

For transparent retry, do not use this obligation alone:

```text
retry output == one new transaction started from the final database state
```

Use the stronger admission test:

```text
retry output must fall outside the set of outcomes reachable by every legal one-attempt execution
under the same transaction snapshot, isolation level, current-read/consistent-read split, and locks
```

A fail-closed counterfactual is not sufficient owner proof. Returning an error can always remove an
allowed behavior. The counterfactual must restore the claimed contract, and the candidate must first
be outside the legal one-attempt outcome set.

## Reopen rule

Reopen this surface only if retry produces a value, terminal result, side effect, or durable state
that no legal single attempt can produce under the same isolation contract. A fresh-state control by
itself is not enough.

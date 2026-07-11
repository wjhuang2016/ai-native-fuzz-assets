# S7 Hidden-Input Getter Scan
> 2026-07-03. Working ledger for prepared/cache payload bugs. The goal is to avoid function-name
> enumeration and instead scan hidden context getters by proof obligation.

## Core Rule

```text
cached payload must be a pure function of:
  explicit SQL inputs
  cache-key dimensions
  hidden session/config inputs consumed while building the payload
```

A missing key dimension is not enough. It becomes a bug only when the cache hit skips the safe path
and reuses a payload derived from the old hidden input.

## Current Scan

| Getter / variable | Consumer shape | Cache behavior | Result | Note |
| --- | --- | --- | --- | --- |
| `GetDefaultWeekFormatMode` / `default_week_format` | `WEEK(date)` without explicit mode, all-constant scalar fold | cache hit reuses folded `Constant` | RED id30034 | First implicit-input constant-fold proof |
| `GetDivPrecisionIncrement` / `div_precision_increment` | decimal `1/7`, all-constant scalar fold | cache hit reuses folded `Constant` | RED id30035 | Proves selector is not date-specific |
| `GetDivPrecisionIncrement` / `div_precision_increment` | `AVG()` type inference over table rows | cache hit reuses aggregate `RetTp.Decimal` inferred under old precision | RED id30036 | New payload class: cached type/descriptor metadata |
| `GetDefaultCollationForUTF8MB4` / `default_collation_for_utf8mb4` | `_utf8mb4` literals without explicit `COLLATE` | cache hit reuses literal field-type collation written during expression rewrite | RED id30037 | New hidden input; connection collation being key-covered is not enough |
| `block_encryption_mode` | `AES_ENCRYPT` 2-arg and 3-arg forms | 2-arg follows current error semantics; 3-arg semantic-changing case did not hit cache | GREEN / uncached | Useful negative: not every omitted variable produces stale payload |
| `group_concat_max_len` | `GROUP_CONCAT` output length | cache hit observed, output followed current session value | GREEN | Boundary/result is rebuilt at execution |
| `GetUserVarsReader` / user variable type & value | `@u` read with collation-sensitive comparison | direct old/new contract exists, but prepared statement did not hit plan cache | GREEN / uncached | `@u` bin -> general_ci changed direct equality 0 -> 1; second EXECUTE had `@@last_plan_from_cache=0` |
| `Rng()` / `RAND()` | volatile random value payload | prepared plan cache hit observed, values changed across executions | GREEN | Cache hit did not freeze the random value |
| `windowing_use_high_precision` | window aggregate precision | cache hit observed; output followed current session value | GREEN | Direct ON/OFF contract exists on cancellation-prone frames, but the cached plan did not freeze the old aggregate implementation |
| `max_allowed_packet` | `CONCAT` family captures packet limit in builtin signature | same-session switch is not practical in the current cluster | SKIP | Low-quality candidate without a switchable oracle |
| `time_zone` | timezone-sensitive folding/boundary setup | key uses current offset, not full timezone rule set | RED id30025 / GREEN calibration | Red only when cached payload depends on omitted historical rules |
| `sql_mode`, charset, collation, `foreign_key_checks` | broad planner/expression semantics | mostly key-covered or explicit | LOW PRIORITY | Use as controls before claiming missing-key bugs |

## Highest-Yield Workflow

```text
1. Getter queue
   List EvalContext / BuildContext getters and session vars consumed during build, rewrite,
   folding, type inference, range construction, request extraction, or executor descriptor setup.

2. Four-way classification
   key-covered
   explicit SQL input
   rebuilt/deferred at execution
   cached payload risk

3. Tiny matrix
   direct old/new contract
   prepare under old value
   switch to new value, require @@last_plan_from_cache=1
   direct current-session reference
   ADMIN FLUSH SESSION PLAN_CACHE reference
   one green control showing the whole feature is not simply broken

4. Selector update
   If red, pause. Record the hidden input, cached payload class, consequence oracle, and stop rule.
   Do not expand within the same syntax family unless it proves a new payload class or user-visible
   consequence.
```

## Why This Works

Source code tells us where the system silently assumes:

```text
code checked P: cache key / cacheability / clone rules say reusable
system believes Q: cached payload is still semantically current
fast path: cache hit skips rebuild / re-inference / re-folding
```

AI is effective here because it can connect distant facts that ordinary fuzzing treats as separate:
which getter is read, whether that value is in the cache key, which object stores the derived value,
and what small SQL matrix exposes the stale payload.

## Next Candidates

- Continue getter-level scan, but require a direct old/new semantic contract before running cache.
- Prefer new payload classes over siblings of id30034/id30035/id30036/id30037.
- Record green cells; they are what keep the selector precise.

## Latest Negative Calibration

`GetUserVarsReader` looked promising because `rewriteUserVariable` builds `GetVar` from the current
`UserVarType`, and `GetVar` execution later reads the current value. The direct contract was real:

```text
SET @u = _utf8mb4'A' COLLATE utf8mb4_bin;
SELECT COLLATION(@u), @u = _utf8mb4'a'; -- utf8mb4_bin, 0

SET @u = _utf8mb4'A' COLLATE utf8mb4_general_ci;
SELECT COLLATION(@u), @u = _utf8mb4'a'; -- utf8mb4_general_ci, 1
```

But the prepared statement did not hit plan cache after the switch (`@@last_plan_from_cache=0`), so
the stale-type payload was not reused.

`RAND()` was the opposite calibration. It did hit plan cache, but successive cached executions
returned different random values. That proves a cache hit alone is not a bug; the oracle must show
semantic payload reuse, not just reuse of a physical plan.

`windowing_use_high_precision` finished the third calibration shape. This time both early gates were
true: a deterministic direct contract existed, and the prepared statement hit plan cache after the
session switch.

```text
windowing_use_high_precision=ON:
  SUM(x) OVER (ORDER BY id ROWS BETWEEN 2 PRECEDING AND CURRENT ROW)
  on [1e16,1,-1e16,1,1e16,1,-1e16,1] -> row5/row7 = 0

windowing_use_high_precision=OFF:
  same direct query -> row5/row7 = -1

prepare under ON, execute -> row5/row7 = 0, cache=0
switch OFF, same EXECUTE -> row5/row7 = -1, cache=1 GREEN
flush session plan cache, same EXECUTE -> row5/row7 = -1, cache=0 GREEN reference
```

This kills the candidate at the stale-payload gate. The cached physical plan is reusable, but the
window aggregate implementation follows the current session setting at execution. The selector
therefore becomes sharper: direct semantic drift plus a cache hit is still not enough; the cached
object must carry the old derived payload.

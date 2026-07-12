# Method case: reuse a validated selector, rebuild the owner-specific oracle

Remote `found_bug`: `id1800003`, confirmed high.

## Why this was fast

The loop did not start from a PR or a historical bug. It reused S35:

```text
local state is still abortable
external owner has already committed
later abort has no compensation
```

A current-source scan found `onAlterTablePlacement`: table metadata is staged locally, then the PD
bundle is published through `context.TODO()` before the DDL transaction commits.

## The small matrix

| Cell | Local terminal state | Metadata | PD | Verdict |
| --- | --- | --- | --- | --- |
| Cancel after PD success | cancelled | p1 | p2 | RED |
| Normal ALTER | synced | p2 | p2 | GREEN |
| Same cancel plus committed-bundle republication | cancelled | p1 | p1 | GREEN |

The local matrix used region r1/r2. Real PD rejected labels absent from the testbed, so the live
matrix used valid replica counts instead: p1 had three voters and p2 had two. This preserved the
proof dimension while respecting the environment.

## What was reusable

- Selector S35 and its stop rules.
- The post-external-success cancellation schedule.
- The rule that PR/issues/history stay closed until independent RED.
- The demand for a normal control and a one-variable compensation control.

## What had to be rebuilt

- The owner profile: PD placement-rule bundles, not resource groups.
- The strong oracle: DDL terminal state plus `SHOW CREATE`, InfoSchema bundle, and PD group.
- The consequence argument: silent reduction of declared replica redundancy.

## Method improvement

Store selectors globally, but store obligations and oracles per durable owner. A prior bug should
not become the next candidate. Its validated selector may generate a new current-source proof
obligation; only a fresh owner-specific RED makes that obligation a bug.

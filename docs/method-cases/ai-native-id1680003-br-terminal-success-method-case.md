# Method Case: a checked error must dominate the terminal result

## Starting point

This target came from current source only. No PR, review finding, issue, or historical fix was used
to select it.

```text
P: code checks failure variable e
Q: the failure branch guarantees operation failure
F: the branch publishes a different variable err as the terminal result
```

The important shift is from **error handling exists** to **error identity reaches the owner that
publishes success**.

## Selector

`CHECKED_ERROR_MUST_DOMINATE_TERMINAL_RESULT`

Apply it when a function:

1. receives a new error or terminal result from an operation;
2. branches on that value;
3. returns, commits, acknowledges, or publishes another value;
4. has an irreversible action or required artifact after the branch.

Build an identity-and-action ledger:

```text
producer -> checked variable -> branch -> published terminal value
                              -> skipped irreversible action/artifact
```

Do not stop at variable-name mismatch. Require a strong coherence oracle:

```text
external success <=> required action occurred and required artifact exists
```

## Minimal matrix

| Scheduler removal | Return implementation | Exit | backupmeta | Verdict |
|---|---|---:|---:|---|
| injected failure | stale `err` | 0 | absent | RED |
| success | current source | 0 | present | GREEN |
| injected failure | checked `e` | 1 | absent | counterfactual GREEN |

## LOOP improvement

Extend the existing P/Q/F pass with terminal ownership:

```text
find P -> infer Q -> identify failure value
  -> trace exact identity to the public terminal owner
  -> name the irreversible action and success artifact
  -> inject at the checked boundary
  -> jointly observe process status and action/artifact
  -> run no-fault and one-variable counterfactual controls
```

This catches bugs that ordinary error-check scanners miss: the check is present and the branch is
taken, but success is still owned by a stale sibling value.

## Why this worked

The source contained five nearly identical high-level branches. The first hit was treated as one
root, then followed to the command boundary and backup artifact rather than counted as five syntax
bugs. That produced a severe user consequence with only three cells.

## Stop rule

After one command-level RED, one real-action GREEN, and one checked-error counterfactual, stop. Keep
the remaining sibling surfaces as blast radius; do not run five copies of the same matrix.

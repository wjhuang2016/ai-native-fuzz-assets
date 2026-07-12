# Method Case: scan/delete rechecks must preserve semantic context

## Starting point

The candidate came from current source, not a PR or historical issue:

```text
P: scan and delete retain the same expiration epoch E
Q: both phases therefore enforce the same DATETIME cutoff
F: each statement independently reloads global time_zone and evaluates FROM_UNIXTIME(E)
```

The missing proof dimension is **interpretation context**. Token equality (`E == E`) does not imply
predicate equality when a mutable context participates in decoding that token.

## Selector

`SCAN_DELETE_CONTEXT_STABILITY`

Apply it to a multi-phase workflow when:

1. phase A materializes identities or candidates under predicate `R(x, token, context_A)`;
2. phase B performs an irreversible action after rechecking `R`;
3. only `token` is carried across the handoff;
4. phase B reloads locale, time zone, collation, SQL mode, schema, policy, or another semantic
   context independently;
5. the action can silently affect current user state.

The proof obligation is not merely “there is a recheck.” It is:

```text
meaning(R_A) == meaning(R_B)
```

for every context component that can change between phases.

## Minimal matrix

| Schedule | Refreshed under scan cutoff | Context at delete | Final row |
|---|---:|---:|---:|
| SQLBuilder-equivalent | current | UTC -> +08 | deleted (RED) |
| actual TTL worker | current | UTC -> +08 | deleted (RED) |
| actual TTL worker control | current | UTC -> UTC | preserved (GREEN) |
| old #41043 control | n/a | change before job | expected rows preserved (GREEN) |

The failpoint is only a deterministic scheduler. The oracle is ordinary SQL state after a real TTL
worker completes.

## Methodology improvement

Extend the LOOP's handoff analysis:

```text
find P -> infer Q -> locate fast/safe path
  -> enumerate carried tokens
  -> enumerate semantic context needed to interpret each token
  -> compare context owners across phases
  -> schedule one mutation between read and irreversible action
  -> use a current-state oracle, then one no-mutation control
```

This gives AI a more powerful question than “does delete recheck the row?”:

> Does the recheck mean the same thing as the scan that selected the row?

## Post-hit dedup lesson

History search found #41043 only after the actual-worker RED. Its regression test changes time zone
before job start and is green on current source. The new schedule changes context inside one job,
after scan and before delete. Post-hit history therefore served as a discriminating control instead
of a discovery seed.

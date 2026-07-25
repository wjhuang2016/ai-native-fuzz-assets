# id3390003: an optional-stage early return must preserve required sibling actions

## Starting proof obligation

```text
P: checksum and analyze are optional, and both are disabled.
Q: no post-processing remains, so the operation may return success.
F: duplicate resolution is a separately required sibling action located after that return.
```

The source signal is stronger than a generic early return. One condition summarizes two named
optional stages, then returns across code that also owns a configured safety action.

## Small matrix

| Checksum | Analyze | Conflict strategy | Duplicate input | Result |
| --- | --- | --- | --- | --- |
| off | off | none | no | valid fast return |
| required | off | replace | yes | conflicts resolved, physical state GREEN |
| off | off | replace | yes | success with record/index split, RED |
| off | off | replace | yes, early return narrowed | conflicts resolved, physical state GREEN |

The matched runtime GREEN changes one optional toggle. The counterfactual GREEN keeps the original
configuration and changes only the source admission boundary.

## Strong oracle

1. Require a successful Lightning exit and completion summary.
2. Read records with `IGNORE INDEX`.
3. read the unique index with `FORCE INDEX`.
4. Run `ADMIN CHECK TABLE`.
5. Count captured conflict records.
6. Verify from logs whether duplicate collection and resolution ran.

Checking only the process exit, row count, or one optimizer-selected query would miss the
corruption.

## Selector improvement

Add `OPTIONAL_SIBLING_EARLY_RETURN_CLOSURE`:

1. Find early returns controlled by disabled feature or stage toggles.
2. Inventory every action later in the same lifecycle phase.
3. Classify each action as optional reporting, required safety, or backend-specific.
4. Build the cross product of "all optional stages off" and "required sibling configured".
5. Add the smallest input that makes the required action observable.
6. Judge the public terminal and the highest persistent state, not branch coverage.
7. For GREEN, enable one optional stage or move only the required action before the return.

Useful code shapes include:

```text
if !audit && !analyze { return success }
... reconcile / validate / repair / resolve ...

if noReportersEnabled { markComplete; return }
... publish fence / cleanup / deduplicate ...
```

## Why it worked

Configuration handling compressed several responsibilities into one Boolean shortcut. Source
inspection exposed the proof debt, and the configured `replace` strategy supplied a direct
contradiction to the shortcut. A two-row unique-key collision made the skipped action visible
through three independent persistent observers.

This was more efficient than broad import fuzzing because the source selected both matrix
dimensions: the optional toggle pair and the independently configured required action.

## Cross-module targets

- backup and restore paths where checksum or reporting toggles surround reconciliation;
- DDL post-processing where analyze or validation toggles surround publication or repair;
- GC and cleanup jobs where metrics or dry-run output surround required deletion fences;
- statistics and index maintenance where optional collection surrounds consistency repair;
- storage ingestion where verification toggles surround deduplication or ownership transfer.

## Stop rule

Do not enumerate more Lightning index types, duplicate positions, or conflict values after the same
early return and repair are proved. Move to a different lifecycle owner. Retire candidates when an
outer owner necessarily performs the required action before success, or when the same toggle
explicitly and safely disables that action.

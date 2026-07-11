# Method Case id30011: first live validation of methodology v2
> 2026-07-02. FLASHBACK DATABASE dangling placement ref. This case exists to measure the v2 workflow itself.

## Timeline (single session, ~15 minutes wall clock)

1. **Target sourcing (ledger-driven, ~5 min).** Two selectors with prior hit records jointly
   nominated the target: id30009's "sibling path reconstructs state and loses a bit" and
   id30005's "create validates existence, other paths have no reverse scan". Applied to the
   question "which DDL path re-materializes references without create-time validation?" →
   restore paths (RECOVER/FLASHBACK). The catalog explicitly allowed revisiting placement
   only via "a container path bypassing the in-use scan" — FLASHBACK DATABASE is exactly that.
2. **Audit card + T_tests (~5 min).** Source read confirmed the asymmetry before any SQL ran:
   `recoverTable` sanitizes refs (`clearTablePlacementAndBundles`), `onRecoverSchema` restores
   DBInfo verbatim. `T_tests` grep: zero tests combine recover/flashback with placement.
   Card scored 15/15; two competing candidates (slow-query time extractor, prefix-index
   re-check) scored lower on oracle-noise and density and were not probed.
3. **Small matrix (~5 min).** 6 cells, pure SQL, no failpoints. First run: 2 red cells.
   Red cell 1: dangling ref visible in SHOW CREATE DATABASE. Red cell 2: hard user-visible
   consequence — CREATE TABLE fails 8239 in the recovered database.
4. **Pause gate.** Expansion stopped; 6-statement minimal repro verified manually; source
   chain + fix direction documented; id30011 written to found_bug (confirmed); selector
   ledger updated.

## What v2 specifically contributed (vs v1 behavior)

- **Ledger-driven sourcing**: the target was nominated by combining two existing selectors,
  not by scanning a new subsystem from scratch. Search space: one function pair, not a module.
- **T_tests field**: the zero-coverage check upgraded confidence before any execution cost.
- **D_dims battery walk**: "same-name recreation" and "ID-vs-name binding" cells were in the
  matrix from the start (INFO cell showed name-bound healing — relevant to fix semantics).
- **Trigger evidence discipline**: the green control cell (`recover_table_clears_ref_control`)
  proves the sanitization design actually fired, so the red cells are a true asymmetry,
  not two paths both unimplemented.
- **Cost**: 6 cells to 2 reds. The prior lane average was tens of cells per red
  (e.g. 28-cell and 17-cell matrices with 0 findings). Sourcing precision, not volume,
  produced the hit.

## Selector validated (ledger entry opened)

```text
restore/undelete/import path re-materializes CONTAINER metadata verbatim
while the sibling OBJECT path sanitizes references
+ create/alter-time validation absent on the restore path
+ reference-liveness oracle
```

Reuse candidates (not probed, pause gate active): BR/IMPORT restore of schema objects,
FLASHBACK TABLE TO new name × other reference owners, EXCHANGE PARTITION as a
"move between containers" variant, recover × TiFlash replica / resource-group fields.

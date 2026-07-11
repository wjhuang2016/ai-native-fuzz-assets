# Method Case id30012: second selector, second hit (v2 efficiency evidence)
> 2026-07-02. cluster_log timezone wrong-result. This case is the n=2 data point for the methodology-efficiency argument.

## Why this case matters for the argument

id30011 proved the v2 workflow runs end to end. The open question was whether hits come
because a selector *predicts*, or because we keep looking everywhere. id30012 answers it:
a **different** selector (S3, born from id30010) predicted a **different** bug in a
**different** subsystem, and the target was found by pure source reasoning, not execution.

## Timeline (single session, ~12 minutes)

1. **Ledger-driven sourcing.** After the id30011 pause gate, instead of expanding restore
   variants (S6 queue, gated), switched selector to test generalization. S3 = "custom
   extractor performs a lossy semantic prefilter and drops the original predicate; a
   no-shortcut/CASE oracle exists." Its queued target was "time-range extractor x session
   time_zone."
2. **Source-level red flag before any SQL.** One grep over the extractor file surfaced an
   asymmetry directly: `extractTimeRange(..., time.Local)` at line 816 for cluster_log,
   versus `StmtCtx.TimeZone()` at 1048/1334/1626 for every sibling extractor. Read confirmed
   the matched time predicate is dropped from `remained` — so a wrong zone is a wrong result,
   not just a wrong scan cost. The bug was essentially proven from source; execution was
   confirmation.
3. **Oracle design.** Absolute-instant equivalence: same literal window under two session
   zones must select different absolute instants. Chose `+00:00` vs `+14:00` for a 14h gap.
4. **Two-directional confirmation.** Forward: `+14:00` returns the identical 415 rows as
   `+00:00` (rows that violate WHERE). Reverse: the tz-respecting `+14:00` literal returns 0
   (drops rows that satisfy WHERE). Both directions, deterministic.
5. **Pause gate.** 1-cell probe frozen; source chain + fix direction documented; id30012
   written to found_bug (confirmed); S3 ledger entry updated to 2/2.

## What v2 contributed vs blind fuzzing

- **Prediction, not search.** The target came from a selector's queued prediction; the
  red flag was a code asymmetry, found by reading one function, not by generating SQL.
- **Sibling-asymmetry heuristic.** "Three siblings do X, one does Y" is the single highest-signal
  pattern for extractor bugs; it belongs in the battery (binding/semantics dimension).
- **Predicate-drop check gated severity.** Confirming the predicate is not re-appended to
  `remained` is what upgraded this from "wrong scan window (perf)" to "wrong result".
- **Cost.** 1 probe cell, ~12 min, source-first. No matrix expansion needed because the
  root cause was visible in source.

## Selector ledger update

```text
S3 (shortcut/extractor lossy prefilter): 2/2 hits (id30010 InfoSchema LIKE collation,
                                          id30012 cluster_log time.Local vs session tz)
```

New battery entry appended (binding/semantics): "sibling extractors pass session tz; one
passes server-local tz" — a reusable sibling-asymmetry red flag for any prefilter family.

## Reuse candidates (not probed; would be their own targets)
- Other memtable extractors that convert literals with a fixed vs session-derived context.
- INSPECTION_RESULT / metrics_summary time bounds under non-UTC session.
- Any extractor that lowercases, fixes a collation, or fixes a zone while dropping the predicate.

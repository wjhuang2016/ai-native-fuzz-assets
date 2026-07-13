# id2070003: cloned canonical/active view identity

Remote bug DB: `found_bug id2070003`, issue-filed high; upstream #69790.

## P / Q / fast path

- P: the alternative logical subtree and each AccessPath were deep-cloned.
- Q: stats derivation and physical planning still observe the same path state inside that clone.
- Fast path: consume the cloned `PossibleAccessPaths` and treat an empty range slice as proof that
  the scan is empty, producing `TableDual`.

Deep cloning proves ownership isolation between alternatives. It does not prove preservation of
the alias relation between canonical and active views inside one alternative.

## Why the method worked

The source proof identified two lists that originally pointed at the same mutable objects but were
cloned independently. The first test matrix changed subquery shapes. Eight cells were GREEN; only
aggregate `IN` was RED because aggregation kept the correlation above the DataSource and disabled
the repair path that normally rebuilt both lists.

That masking dimension was more useful than adding SQL syntax. It explained why ordinary correlated
queries passed and provided the smallest selector refinement: vary whether a downstream owner
reconstructs the shared view.

## Strong oracle

Use feature OFF as the no-alternative reference, feature ON as the candidate, and a plain correlated
IN query as the adjacent control. Compare both rowsets and plan altitude. The RED signature is
`OFF=[1,2,3]`, `ON=[]`, and `HashAgg -> TableDual`; the exact alias-preserving counterfactual keeps
Apply but restores the real scan and rowset.

## Reusable selector

For every clone/copy routine, inventory slices, maps, indexes, caches, and filtered views that
reference the same mutable objects. Build an alias graph before and after cloning. Rank a candidate
when one view is populated or refreshed and another is consumed by a shortcut. Vary whether an
intermediate repair/rebuild path runs; a passing repaired shape can be the mask that reveals the
failing unrepaired shape.

# Method Case: target-generation lineage behind a completed checkpoint

## Proof obligation

`P`: completed checkpoint state belongs to the current target table generation.

`Q`: same table name and checkpoint location are treated as sufficient identity.

`F`: old terminal status dominates chunk population, engine import, checksum, and analyze for a
new same-name table.

## Why the method worked

The selector did not enumerate another input filename variant of id1890003. It followed a distinct
semantic owner: the target TiDB table generation. Current source supplied two unusually strong
signals: a declared hash guard fed a constant value, and a file-driver TODO at the exact admission
boundary.

The matrix then changed one dimension only. Fresh/current and retained/same-generation were GREEN;
retained/recreated-generation was RED locally and in a real Lightning process. The strongest oracle
was not `ADMIN CHECK TABLE`, but process success joined with TableID and expected source rowset.

## Method improvement

For persisted skip tokens, split lineage into source lineage, target generation, configuration, and
artifact lineage. A fingerprint for one owner does not prove the others. Also verify that the chosen
identity field is actually materialized in every backend; the first TableID counterfactual was
locally sound but live-invalid because TiDB backend stored both identities as zero.

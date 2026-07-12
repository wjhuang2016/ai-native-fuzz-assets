# id1890003: S39 on an input-data owner

P: `Finished` means the current table input was imported. Q: table name and restored group key are
treated as proof of the same run. F: the checkpoint carries no input fingerprint, and the importer
can restore the old group key for a new invocation.

The useful test dimension was not group-key inequality: production code makes the keys equal. The
AI corrected the initial selector and changed the hidden semantic owner instead, replacing the input
files while preserving table name, checkpoint path, and restored group. This is the two-lineage
matrix S39 requires.

The highest oracle is public orchestration: nonempty current input, `SubmitAndWait=nil`, and exactly
zero submissions. No-checkpoint submission and finished-resume baseline are controls.

# Lightning importinto trusts a finished checkpoint from another input run

Status: confirmed on current master `13282a8bd06b`; remote bug DB `id1890003`, high severity.

`TableCheckpoint` is keyed by table name and persists `JobID`, `Status`, and `GroupKey`, but no
source-file/config/target fingerprint. `submitAllJobs` returns immediately for any `Finished` row.
The importer restores the old group key, and its successful cleanup only handles
`keep-after-success=remove`; retained checkpoints remain at the same lookup location.

Local RED: an old finished checkpoint for `db.t` plus current nonempty `new-lineage.csv` makes
`SubmitAndWait` return nil with `SubmitTable` count 0. With no checkpoint, the same input is submitted
once. The existing finished-resume baseline remains GREEN.

Fix by binding checkpoints to a normalized input/config/target fingerprint and validating it before
the finished/running fast paths. Implement rename semantics for the importinto backend or reject the
unsupported keep mode. Discovery used current source only; issue search happened after RED.

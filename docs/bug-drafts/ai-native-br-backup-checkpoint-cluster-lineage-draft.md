# BR backup checkpoint is not bound to the source cluster

Status: confirmed on current master `13282a8bd06b`; remote bug DB `id1920003`, high severity.

`CheckpointMetadataForBackup` stores `GCServiceId`, `ConfigHash`, and `BackupTS`, but no actual PD
cluster ID. The config hash contains PD address strings. A retry loads the old metadata, accepts the
same hash, reuses its `BackupTS`, publishes checkpoint SST files into the new `backupmeta`, and marks
matching current key ranges complete.

Local consumer RED used current client cluster ID 222 and an old completed range with
`old-cluster.sst`. `CheckCheckpoint` returned nil, requested BackupTS 200 became 100, the current
incomplete-range set was empty, and the flushed backupmeta contained the old file. With no
checkpoint, the same current range remained incomplete and would be submitted. Adding only a
checkpoint `ClusterID=111` and comparing it at `CheckCheckpoint` rejected the retry before a
backupmeta was written.

A production-shaped trigger is an interrupted checkpoint backup whose storage prefix survives a
PD/TiKV cluster rebuild or replacement behind the same DNS or configured PD addresses. Persist and
validate the source cluster ID before consuming any checkpoint timestamp, range, checksum, or file.
Legacy checkpoint metadata without the field needs an explicit fail-closed or migration policy.

Discovery came from current source only. Asset and GitHub issue searches were performed only after
the independent RED and found no exact root.

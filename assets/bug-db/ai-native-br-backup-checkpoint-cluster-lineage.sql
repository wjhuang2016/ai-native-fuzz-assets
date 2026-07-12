INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(1920003,
 'BR backup retry can publish SST files from a replaced source cluster',
 'high',
 'data_loss',
 'br backup full',
 'backup checkpoint resume',
 'After a source cluster is replaced behind the same PD address strings, retrying an interrupted checkpoint backup can report success while the backupmeta references SST files from the old cluster and skips the matching ranges in the current cluster.',
 'Start a checkpoint-enabled backup of cluster A to a storage prefix and interrupt it after at least one range is checkpointed. Replace the PD/TiKV cluster with cluster B behind the same configured PD addresses, retain the prefix, and retry the same BR command. Inspect the retry BackupTS, incomplete-range set, and final backupmeta data files.',
 'Checkpoint metadata is accepted only when its recorded source cluster identity matches the current PD cluster. A mismatch must fail before reusing BackupTS, ranges, checksums, or SST files.',
 'CheckCheckpoint accepts the same config hash without a cluster identity check, GetTS reuses the old BackupTS, BuildProgressRangeTree reports zero incomplete current ranges, and the new backupmeta contains old-cluster.sst.',
 'CheckpointMetadataForBackup stores config hash and BackupTS but no actual PD cluster ID. The hash binds PD address strings rather than the cluster behind them. Retry therefore treats old range completion and SST files as current-lineage proof.',
 'Persist the source PD cluster ID in backup checkpoint metadata and compare it with Client.clusterID before any checkpoint-derived timestamp, checksum, range, or file is consumed. Define an explicit compatibility policy for legacy metadata that lacks the field.',
 'br-backup-current-cluster-range-and-file-lineage',
 'current-source-proof-local-consumer-matrix',
 'br-backup-checkpoint-unbound-source-cluster',
 'master 13282a8bd06b',
 1,
 'confirmed',
 NULL,
 'No PR/review/issue/history was used to generate the candidate. Local RED: CheckCheckpoint passed for current client clusterID 222, requested BackupTS 200 became checkpoint 100, incomplete ranges were 0, and backupmeta contained old-cluster.sst. No-checkpoint control produced one current range. A one-field ClusterID guard rejected 111 versus 222 before backupmeta publication. Four post-RED GitHub issue searches returned no exact root.');

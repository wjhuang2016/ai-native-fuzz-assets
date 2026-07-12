package backup

// Reusable shape: persist a full-range backup checkpoint containing an
// old-lineage SST, load it into a Client whose current cluster ID differs, and
// execute CheckCheckpoint -> GetTS -> BuildProgressRangeTree -> FlushBackupMeta.
// RED requires all of: admission succeeds, the old BackupTS wins, current
// incomplete ranges are empty, and backupmeta contains the old SST. The
// no-checkpoint control must leave the current range incomplete. The one-field
// counterfactual binds source ClusterID and rejects before artifact publication.

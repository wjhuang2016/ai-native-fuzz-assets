package importinto_test

// Reusable shape: return a Finished TableCheckpoint for db.t, pass a current
// TableMeta with a different nonempty DataFiles path, and count JobSubmitter calls.
// RED is SubmitAndWait=nil with calls=0. The no-checkpoint control must call once.

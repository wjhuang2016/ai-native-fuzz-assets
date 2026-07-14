INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2250003,
 'Async commit can return txn-too-old error and later recover as committed',
 'high',
 'atomicity',
 'COMMIT',
 'client-go async commit / transaction age limit',
 'COMMIT returns the ordinary txn takes too much time error, but if asynchronous cleanup does not finish, a later reader can recover the complete async lock set as committed. A caller that retries the logical operation can therefore apply it twice.',
 'Use the client-go integration probe TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted. Make only the MaxTxnTimeUse oracle check report expired, enable tikvclient/commitFailedSkipCleanup, commit a two-key async transaction, assert the ordinary age error, then point-get both keys through the normal lock resolver. The same binary passed against testbed 8220955 with one PD and three real TiKV nodes.',
 'An ordinary commit error must imply that the write set cannot later become committed. Once successful async prewrite has crossed the recovery commit frontier, a late guard must run before that frontier or return success/explicit undetermined unless rollback is synchronously proven.',
 'All async prewrites succeeded and minCommitTS was nonzero. The post-prewrite age check returned an ordinary error. With cleanup unavailable, CheckTxnStatus/CheckSecondaryLocks derived a nonzero commitTS and ResolveLock completed with action=commit for both keys; both values became visible.',
 'twoPhaseCommitter.execute runs MaxTxnTimeUse validation after prewriteMutations succeeds and after async minCommitTS is selected. The deferred error path treats it as an abort and launches best-effort cleanup, but complete async locks are already sufficient for LockResolver to recover commit when that cleanup is unavailable.',
 'For async commit, run the transaction-age check before prewriteMutations and do not return that ordinary error after successful async prewrite. A local counterfactual at exactly this altitude kept both keys absent. Review 1PC age policy separately.',
 'TERMINAL_ERROR_VS_INDEPENDENT_MVCC_RECOVERY_TRUTH',
 'POST_PROOF_FALLIBLE_EPILOGUE',
 'async-commit-age-check-after-recovery-proof',
 'client-go 01bd8f99; TiDB b8d04e17; real TiKV confirmed',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69831',
 'Discovery used current source only. Three post-RED GitHub issue searches found no exact root. Production reachability requires async commit explicitly enabled (default OFF), a transaction older than 24 hours, successful prewrites, and cleanup delayed beyond lock expiry by TiKV-path loss or TiDB termination. An application retry on another TiDB can recover the first attempt as committed and then apply the operation again. Local RED, local fix GREEN, and real-TiKV RED evidence are stored in the private AI-native asset repository. Issue #69831 contains the directly runnable integration probe and found-by-ai/severity-critical/component-tikv-client labels.');

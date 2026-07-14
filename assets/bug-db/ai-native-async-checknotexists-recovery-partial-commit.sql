INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2550003,
 'Async commit can commit business writes after a duplicate-key proof fails',
 'high',
 'atomicity/data-integrity',
 'COMMIT',
 'optimistic transaction / lazy uniqueness / async commit recovery',
 'COMMIT returns a definite duplicate-key error, but an unrelated write from the same transaction can later be recovered as committed. Retrying the failed business operation can apply that write twice.',
 'Enable async commit and disable 1PC. In an optimistic transaction, insert a row whose unique value already exists, delete that just-inserted row, and update an account in another table/Region. Let cleanup be interrupted after COMMIT returns the duplicate error. After the primary lock expires, read the account from another session.',
 'A definite duplicate-key error aborts every write in the transaction; the account remains unchanged.',
 'The duplicate error was returned, but the fresh read made LockResolver recover the async primary as committed and observed the account balance changed from 0 to -100. The candidate table still contained only the original unique row.',
 'Optimistic insert-then-delete turns lazy uniqueness keys into Op_CheckNotExists. client-go allows the transaction to use async commit but excludes every CheckNotExists key from the primary lock secondary list. The business primary can prewrite successfully in one Region while the proof batch returns AlreadyExist in another. If background cleanup is unavailable, the empty recovery certificate makes LockResolver choose commit without observing the failed proof.',
 'Close the proof set before selecting async commit. The minimal counterfactual is to reject async commit when hasNoNeedCommitKeys is true. A more general fix may durably represent proof-only mutations in the recovery certificate so a failed proof prevents commit recovery.',
 'DUPLICATE_ERROR_VS_FRESH_CROSS_TABLE_DURABLE_STATE',
 'RECOVERY_CERTIFICATE_PROOF_CLOSURE',
 'async-recovery-omits-failed-checknotexists-proof',
 'current TiDB b8d04e17a2ca / client-go 01bd8f99 / real TiKV; MDL default ON; async commit enabled',
 1,
 'confirmed',
 NULL,
 'Discovered from current source and a recovery-certificate proof obligation without PR or issue seeds. Real-TiKV SQL RED reproduced 3/3 without Region-delay injection. Cleanup skip models TiDB exit, OOM, rolling restart, or exhausted cleanup RPC retries after the duplicate response. Exact client-go and end-to-end SQL owner counterfactuals were GREEN. Post-RED GitHub searches found no exact root; #65757 is a distinct stale-secondary-lock/minCommitTS issue.');

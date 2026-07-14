INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2670003,
 'Autocommit retry mixes an old explicit ID with a new row payload',
 'high',
 'atomicity/data-integrity',
 'INSERT SELECT',
 'autocommit retry / explicit auto-ID / dynamic source',
 'A successful INSERT SELECT retry durably combines an explicit entity ID from the failed attempt with the payload read by the successful attempt.',
 'Run an autocommit INSERT INTO target(id,payload) SELECT target_id,payload FROM staging ORDER BY slot ON DUPLICATE KEY UPDATE over a stable staging slot mapped to target_id 100/payload old. While the first attempt is in flight, let an incremental reconciliation transaction correct that slot to target_id 200/payload new and update another hot target row covered by the batch. The batch gets a real 9007 conflict on that hot row and TiDB transparently retries. Classic default pessimistic-auto-commit=false, autocommit=1, tidb_retry_limit=10, MDL ON, one TiDB, one TiKV, and two sessions are sufficient; no failpoint or node failure is required.',
 'The only coherent one-attempt outputs are target(100,old) if the batch wins first or target(200,new) if reconciliation wins first. The forced retry must commit target(200,new).',
 'The statement returned success with Exec_retry_count=1. A fresh session read staging target_id=200/payload=new but target id=100/payload=new; ADMIN CHECK TABLE remained green.',
 'RetryInfo stores explicit nonzero auto-increment inputs in the same positional autoIncrementIDs buffer as generated IDs. adjustAutoIncrementDatum consumes the old positional value before parsing the current retry datum, so a re-executed source row with explicit ID 200 is overwritten by cached ID 100 while its new payload is retained.',
 'Classify the current datum before cache reuse and retain provenance/owner binding for retry IDs. Reuse cached values only when the current input requires generated allocation for the same logical row; otherwise consume the old slot for alignment but use and validate the current explicit ID.',
 'ALLOWED_SINGLE_ATTEMPT_SET_PLUS_FRESH_SOURCE_TARGET_ANTI_JOIN',
 'RETRY_CACHE_PROVENANCE_AND_IDENTITY',
 'retry-autoid-positional-cache-overwrites-current-explicit-id',
 'TiDB 2964713e / real TiKV nightly; classic defaults; MDL ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69845',
 'Generated from current source, not a PR-review finding. Real-TiKV RED and exact owner GREEN are recorded. Post-RED dedup found historical #20629/#20659, which covers generated-ID buffer exhaustion and error avoidance, not silent explicit-ID/payload recombination. Upstream #69845 includes the concrete production trigger and directly runnable real-TiKV test.');

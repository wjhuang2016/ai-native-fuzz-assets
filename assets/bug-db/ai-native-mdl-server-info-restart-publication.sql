INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2700003,
 'Failed server-info restart can let MDL DDL corrupt a secondary index',
 'high',
 'atomicity/data-integrity',
 'ALTER TABLE ADD INDEX',
 'metadata lock / server-info lease restart / explicit transaction',
 'A live TiDB disappears from DDL membership after one failed server-info restart. ADD INDEX and an old transaction both return success, but the committed row has no index entry.',
 'On TiDB A, begin a pessimistic transaction and insert one row. Let only A''s 90-second server-info etcd session end while its schema-sync session and SQL connection remain live. A creates a replacement session, but all five StoreServerInfo retries fail during a short etcd recovery flap; the replacement lease then remains healthy. Before that lease ends, TiDB B runs ADD INDEX and A commits the old transaction. MDL remains at its default ON value. The real-TiKV reproducer closes exactly the server-info session, fails exactly the first restart publication, then lets etcd recover.',
 'A failed registration must retain a retry trigger or fail closed. DDL must include every live old-schema TiDB in its MDL wait set, or the old transaction must fail schema validation.',
 'Restart logged mock store server info error and never retried because the newly assigned session remained live. ADD INDEX succeeded, COMMIT succeeded, a table scan returned (1,10), a forced idx_v scan returned no rows, and ADMIN CHECK TABLE returned 8223.',
 'NewSessionAndStoreServerInfo assigns s.session before StoreServerInfo succeeds. If StoreServerInfo exhausts its retries, ServerInfoSyncLoop returns to select on the live replacement session instead of the completed old session, so no retry republishes /tidb/server/info. MDL DDL builds its wait set from that key. Meanwhile MDL transactions set needCheckSchemaByDelta=false, so the old transaction trusts the missing DDL wait and commits against the new schema.',
 'Publish the replacement session only after its server-info key is stored. On publication error, close the replacement and retain the completed prior session as the retry trigger, or make the loop explicitly retry registration with backoff until success or shutdown.',
 'MDL_MEMBERSHIP_ROW_INDEX_CLOSURE',
 'FAILED_PUBLICATION_LIVE_OWNER',
 'server-info-restart-publishes-live-session-before-registration',
 'TiDB 2964713e / real TiKV nightly; classic defaults; MDL ON; server-info TTL 90s',
 1,
 'confirmed',
 NULL,
 'Current-source discovery, not a PR-review finding. The real-TiKV RED logs the exact failed StoreServerInfo call before row/index divergence. The exact owner counterfactual retries registration, makes DDL wait, and finishes 1/1 with ADMIN CHECK green. A whole-process 95-second stall is GREEN because schema-sync restart advances restartSchemaVer and COMMIT returns 8028; production reachability therefore requires server-info lease loss while schema-sync remains live. Post-RED issue searches found no exact root.');

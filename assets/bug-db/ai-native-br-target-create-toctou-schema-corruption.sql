INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3090003,
 'BR can restore into a concurrently created incompatible table and report success with a corrupted index',
 'high',
 'data-corruption',
 'br restore table',
 'target existence admission and schema identity',
 'A table created after BR target-existence precheck but before BR schema creation is silently accepted. BR maps backup indexes to the concurrent table by index name, reports restore and checksum success, and can leave point lookups returning rows that do not satisfy their own predicates.',
 'Back up t(id PK,a,b,UNIQUE uk(a)). Drop t and start br restore table with default checkpoint mode. After the precheck has passed and checkpoint initialization begins, concurrently create t(id PK,a,b,UNIQUE uk(b)). Wait for BR success, then force uk for WHERE b=10 and run ADMIN CHECK TABLE.',
 'The restore must atomically claim the target table identity. If another actor creates the name after admission, BR must fail before generating rewrite rules or ingesting KVs unless the exact expected schema and identity are proven compatible.',
 'Two independent runs exited 0, reported Table Restore success, and validated checksum. The table kept uk(b), but its restored index keys encoded backup column a. WHERE b=10 returned a row with b=100, WHERE b=100 returned no row, ADMIN CHECK returned 8223, and UPDATE ... WHERE b=10 successfully modified the b=100 row.',
 'checkTableExistence proves absence only at one InfoSchema snapshot. BR later calls BatchCreateTableWithInfo with OnExistIgnore, so a concurrent CREATE is treated as successful BR creation. SnapClient then reacquires the table by name, checks only IsCommonHandle, and GetIndexIDMap maps indexes solely by name before physical ingest.',
 'Use an atomic target-name claim or make BR table creation fail on existence after the precheck. Bind every rewrite rule to the exact table ID created by BR. If idempotent reuse is required, compare a full restore-relevant schema fingerprint, including columns, types, defaults, generated expressions, primary handle, indexes, constraints, and special metadata, before ingest.',
 'br-terminal-schema-fingerprint-record-index-predicate-closure',
 'proof-obligation-small-matrix-strong-oracle',
 'br-target-absence-check-idempotent-create-identity-gap',
 'master 05b396fb6636; verified BR a942e4684f43',
 1,
 'confirmed',
 NULL,
 'Default/common configuration: one TiDB, one PD, one real TiKV, checkpoint enabled, MDL=ON, no failpoint, source patch, process pause, node fault, or network/disk fault. The competing action is ordinary CREATE TABLE during a selective restore. The verified BR revision and current master have no relevant source diff. Historical #35215/#42893/#55087 and PR #55044 cover a table that exists before restore precheck; post-RED searches found no exact check-to-create race or incompatible-schema success root.');

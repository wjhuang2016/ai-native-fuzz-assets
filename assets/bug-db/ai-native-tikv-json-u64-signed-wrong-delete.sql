INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3600003,
 'TiKV can turn JSON-to-SIGNED overflow into negative values and silently delete rows',
 'high',
 'data-loss',
 'DELETE predicate pushdown',
 'JSON UNSIGNED INTEGER to SIGNED cast',
 'A strict DELETE that TiDB evaluates as overflow error 1690 can succeed after predicate pushdown. TiKV wraps JSON unsigned integers above MaxInt64 into negative signed values, selects those rows, and permanently deletes them while ADMIN CHECK TABLE remains green.',
 'Create matched event tables with JSON payload.account_id values 42, 9223372036854775808, and 18446744073709551615. Run DELETE WHERE CAST(JSON_EXTRACT(payload,''$.account_id'') AS SIGNED)<0 on one table. On the other, add the always-false OR RAND()<0 root barrier. Verify plans, statement terminals, ROW_COUNT, and named preimages.',
 'Both execution altitudes preserve the same conversion and strict-DML terminal. The overflow returns 1690 before mutation, leaving all three event preimages.',
 'With TiKV pushdown, DELETE succeeds, affects two rows, and leaves only the ordinary ID 42. With TiDB root evaluation, the same predicate returns 1690, affects zero rows, and preserves all three records.',
 'TiDB routes JSONTypeCodeUint64 through bounded ConvertUintToInt. TiKV uses get_u64() as i64, which wraps values above MaxInt64 into ordinary negative values before EvalContext can handle overflow. The later bounded i64 conversion cannot recover the erased U64 overflow.',
 'Replace the unchecked cast with TiKV''s existing get_u64().to_int(ctx,tp) conversion. Add conformance tests for JSON numeric variants under warning and strict flags, plus pushed Selection and persistent DML coverage.',
 'PUSHDOWN_ERROR_VALUE_TERMINAL_PREIMAGE',
 'PUSHDOWN_EXCEPTION_TO_VALUE_CLOSURE',
 'tikv-json-u64-to-signed-unchecked-wraparound',
 'TiDB master 05b396fb66; TiKV master 91ccfb2126; recent real TiKV 730be34f95; default strict mode; MDL ON',
 1,
 'confirmed',
 NULL,
 'Direct critical-class persistent data-loss consequence under default configuration. Trigger requires a JSON unsigned integer above MaxInt64 and an explicit SIGNED cast in a pushed predicate, but no failpoint, concurrency, retry, process restart, network/disk fault, MDL change, unusual isolation, or nondefault SQL mode. Current TiDB master plus real TiKV is RED. Current TiKV master fails a focused test with -1 instead of the bounded result; routing the same branch through existing checked conversion is GREEN. Post-RED searches found no exact upstream or internal root; TiDB #57848 is a distinct JSON-to-DOUBLE comparison issue.');

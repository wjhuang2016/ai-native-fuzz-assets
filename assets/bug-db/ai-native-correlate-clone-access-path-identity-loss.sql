INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2070003,
 'Alternative logical plan can turn a non-empty aggregate IN subquery into TableDual and return no rows',
 'high',
 'wrong_result',
 'SELECT',
 'alternative logical plans / CorrelateSolver',
 'With tidb_opt_enable_alternative_logical_plans enabled, an aggregate IN subquery over a nonempty table can be planned as TableDual and silently return an empty rowset.',
 'Create o(id,a) with rows (1,1),(2,2),(3,3) and i(a,b) with matching values plus KEY ia(a). Compare SELECT id FROM o WHERE id<=3 AND a IN (SELECT MAX(a) FROM i GROUP BY b) ORDER BY id with tidb_opt_enable_alternative_logical_plans OFF and ON. Also compare the adjacent non-aggregate IN control.',
 'Changing only the alternative-logical-plan switch must not change the SELECT rowset. Both modes should return ids 1,2,3 and the aggregate inner side must scan the real table.',
 'On current master and testbed 8220955 at default cost factors, OFF returned 1,2,3. ON chose Apply -> HashAgg -> TableDual(rows:0) and returned no rows. The adjacent non-aggregate IN query returned 1,2,3 in both modes.',
 'cloneDataSource deep-clones AllPossibleAccessPaths and PossibleAccessPaths independently. Stats derivation fills range state through the canonical AllPossibleAccessPaths objects, while physical planning consumes the separately cloned active PossibleAccessPaths objects. For aggregate IN, correlation remains above HashAgg, so resetStatsForCorrelatedDS does not rebuild the leaf DataSource paths; the active clones retain empty ranges and find_best_task converts the real scan to TableDual.',
 'Clone the canonical access paths once and map every active path to the corresponding clone from that canonical set. Preserve independence between alternative plans while preserving alias identity between canonical and active views inside each cloned DataSource.',
 'ALTERNATIVE_PLAN_ROWSET_AND_SCAN_ALTITUDE_DIFFERENTIAL',
 'CLONED_CANONICAL_ACTIVE_VIEW_IDENTITY',
 'correlate-clone-breaks-active-access-path-alias',
 'current master 13282a8bd06b and testbed 8220955 version 5c9198e948',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69790',
 'Discovery came only from current-source proof obligations and a bounded feature OFF/ON matrix. PR reviews, issues, fixes, and history were excluded until after local RED and exact-fix GREEN. Post-RED searches found no exact root.');

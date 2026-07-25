INSERT INTO found_bug (
  id, title, severity, category, ddl_op, feature, symptom, repro,
  expected, actual, root_cause, fix_hint, oracle, method,
  root_cause_id, affects, confirmed, status, issue_url, notes
) VALUES (
  3330003,
  'TiKV DOUBLE-to-UNSIGNED half-tie rounding drift can silently delete rows',
  'high',
  'data-loss',
  'DELETE/UPDATE',
  'coprocessor expression pushdown',
  'A predicate using CAST(DOUBLE AS UNSIGNED) can select a .5 row in TiKV even though TiDB evaluates the same cast and predicate differently. Ordinary DELETE can permanently remove that row.',
  'Create t(id INT PRIMARY KEY,x DOUBLE), insert (1,0.5),(2,0.4),(3,1.4), then compare WHERE CAST(x AS UNSIGNED)=1 with the same predicate wrapped in IF(SLEEP(0)=0,...,NULL). Run DELETE on reset copies and compare affected and remaining IDs.',
  'A deterministic cast must preserve exact row membership across TiDB and TiKV, and DML must not mutate a row that fails the TiDB SQL predicate.',
  'The pushed predicate selected ids 1,3 while root TiDB selected only id 3. Returned id 1 projected cast_value=0 and predicate_holds=0. Pushed DELETE removed ids 1,3; root DELETE removed only id 3 and preserved id 1.',
  'TiDB ConvertFloatToUint calls RoundFloat, which uses math.RoundToEven. TiKV f64 ToInt::to_uint calls Rust round(), which rounds exact halves away from zero. The cast remains eligible for TiKV pushdown.',
  'Use the same nearest-even conversion primitive in TiKV as TiDB, and add cross-engine exact-half parity cases. Until aligned, do not push the affected cast.',
  'pushdown-root-rowset-self-predicate-dml-preimage',
  'primitive-diff-directed-cross-engine-differential',
  'tikv-float-to-uint-half-tie-rounding-semantic-drift',
  'TiDB nightly ed2376acc6 and master 05b396fb66; TiKV nightly 730be34f95 and master 91ccfb2126',
  1,
  'confirmed',
  NULL,
  'Default strict sql_mode, MDL ON, one TiDB, one PD, one real TiKV, no failpoint, source patch, concurrency, retry, or infrastructure fault. Current TiKV master failed a temporary compatibility assertion for 0.5: expected TiDB value 0, actual 1. Post-RED GitHub and found_bug searches found no exact root.'
);

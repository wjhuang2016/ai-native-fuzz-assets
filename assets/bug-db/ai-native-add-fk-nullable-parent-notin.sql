INSERT INTO found_bug (
  id, title, severity, category, ddl_op, feature, symptom, repro,
  expected, actual, root_cause, fix_hint, oracle, method,
  root_cause_id, affects, confirmed, status, issue_url, notes
) VALUES (
  3360003,
  'ADD FOREIGN KEY can publish historical orphan rows when the referenced key contains NULL',
  'high',
  'data-integrity',
  'ADD FOREIGN KEY',
  'existing-row foreign-key validation',
  'ALTER TABLE succeeds and publishes a foreign key even though existing child rows have no referenced parent. New orphan writes are rejected, while the historical orphan remains durable and ADMIN CHECK TABLE reports no error.',
  'Create a parent table with nullable UNIQUE business_key and rows (1,1),(2,NULL). Create a child table with parent_key values 1 and 2. ADD FOREIGN KEY(parent_key) REFERENCES parent(business_key), then compare the public constraint with a NOT EXISTS orphan query.',
  'ADD FOREIGN KEY must reject any existing non-NULL child key that has no matching referenced key.',
  'The ALTER succeeds, the foreign key becomes public, and one historical orphan remains. Removing only the parent NULL makes the same ALTER fail with error 1452.',
  'checkForeignKeyConstrain builds child_tuple NOT IN (SELECT referenced_tuple). SQL three-valued logic makes an absent child key evaluate UNKNOWN when the referenced subquery contains NULL, so the validator returns no violating row.',
  'Use a correlated NOT EXISTS anti-join with equality on every foreign-key column, preserving the existing child IS NOT NULL filter. Add nullable referenced-key single and composite controls.',
  'validator-not-in-vs-not-exists-public-fk-orphan',
  'proof-obligation-null-boundary-directed',
  'add-fk-validator-not-in-null-poisoning',
  'TiDB nightly ed2376acc6 and current master 05b396fb66 source',
  1,
  'confirmed',
  NULL,
  'Default strict sql_mode, MDL ON, foreign keys enabled, foreign_key_checks=1, one TiDB, one PD, one real TiKV. No concurrency, failpoint, retry, source patch, or infrastructure fault is needed for the product RED. A temporary current-master regression test failed on original code and passed after replacing NOT IN with correlated NOT EXISTS; all temporary source edits were removed.'
);

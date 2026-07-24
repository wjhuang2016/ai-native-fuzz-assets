-- Remote found_bug mirror for id3000003.
INSERT INTO found_bug (
  id, title, severity, category, ddl_op, feature, symptom, repro, expected, actual,
  root_cause, fix_hint, oracle, method, root_cause_id, affects, confirmed, status, notes
) VALUES (
  3000003,
  'Parent-first batch DROP TABLE can leave persistent foreign-key orphans',
  'high',
  'DDL',
  'DROP TABLE',
  'foreign key',
  'Concurrent RENAME can preserve a child after its parent is successfully dropped; writes during the missing-parent interval create durable orphan rows.',
  'Create p and FK child c. Run DROP TABLE IF EXISTS p,<filler tables>,c. After p disappears and before c is reached, run RENAME TABLE c TO c_survivor. Both statements return success. Insert another child row while p is absent, recreate p with a different key, and anti-join child to parent.',
  'A parent drop must not become durable while a referring child can survive the same admitted batch.',
  'The parent is absent, c_survivor retains REFERENCES p(id), two orphan rows persist after p is recreated, and ADMIN CHECK TABLE remains green.',
  'The batch precheck and every parent job ignore all child names in the complete request, although objects are committed as independent sequential DDL jobs. A mutable future child name is treated as an already-completed effect.',
  'Make the batch atomic or exempt only identity-bound children already removed by committed jobs; revalidate latest FK references before publishing the parent drop.',
  'O68_FK_EDGE_CLOSURE_AFTER_BATCH_TERMINAL',
  'S69_FUTURE_SIBLING_EFFECT_AS_ADMISSION_PROOF',
  'drop-table-fk-future-sibling-admission',
  'current master 231dad5225 and official nightly; MDL ON and foreign_key_checks ON',
  1,
  'confirmed',
  'Current-master and nightly RED; child-first matched GREEN; no failpoint/source change/process pause/node failure. GitHub and remote-root dedup completed after RED.'
);

# Severe Bug Index

This index tracks the high-severity bug assets that should guide future mining.

Source of truth for status and severity is the remote `found_bug` table. This file is curated so each row also points to the most useful local asset in this repository.

Last verified: 2026-07-13

- Remote `found_bug`: `MAX(id)=1890003`, `COUNT(*)=102`, `COUNT(DISTINCT root_cause_id)=79`
- High-severity entries: 27 total; 25 confirmed/issue-filed; 1 candidate; 1 known-root rediscovery

## Confirmed / Issue-Filed High-Severity Assets

| ID | Status | Category | Root cause ID | User-visible impact | Primary asset | Method asset |
| --- | --- | --- | --- | --- | --- | --- |
| 600001 | confirmed | data-loss | `reorg-partition-identity-fastpath` | `REORGANIZE PARTITION` can silently drop duplicate nonclustered rows after `EXCHANGE PARTITION`. | [draft](../bug-drafts/ai-native-reorg-duplicate-rowid-drop-draft.md) | [case](../method-cases/ai-native-id600001-method-case.md) |
| 630013 | issue-filed | DDL correctness | `modify-reorg-check-bypass` | `MODIFY COLUMN` can leave rows violating an existing `CHECK` constraint. | [draft](../bug-drafts/ai-native-check-constraint-modify-column-reorg-bypass-draft.md) | [case](../method-cases/ai-native-id630013-method-case.md) |
| 630014 | issue-filed | DDL stale side state | `exchange-idswap-orphan` | `EXCHANGE PARTITION` can orphan masking policies after table ID swap. | [draft](../bug-drafts/ai-native-exchange-partition-masking-policy-orphan-draft.md) | [case](../method-cases/ai-native-id630014-method-case.md) |
| 1230001 | confirmed | wrong-result | `ntdml-tx-read-ts-split-range-stale` | Non-transactional DML can silently use stale `tx_read_ts` split range and miss current rows. | [draft](../bug-drafts/ai-native-ntdml-tx-read-ts-stale-split-draft.md) | [case](../method-cases/ai-native-id1230001-method-case.md) |
| 1290001 | confirmed | DDL liveness | `addindex-fastreorg-pd-tso-retry-misclassified-fatal` | Fast reorg `ADD INDEX` rolls back on transient PD TSO stream retry timeout instead of retrying. | [draft](../bug-drafts/ai-native-fast-reorg-pd-tso-retry-timeout-draft.md) | [catalog](../core/ai-native-proof-obligation-catalog.md) |
| 1320001 | confirmed | DDL liveness | `ddl-ingest-retryable-kv-family-misclassified-fatal` | Ingest-mode `ADD INDEX` / `ADD PRIMARY KEY` roll back on retryable leader-change family instead of retrying. | [draft](../bug-drafts/ai-native-ingest-retryable-fault-rollback-draft.md) | [catalog](../core/ai-native-proof-obligation-catalog.md) |
| 1350001 | confirmed | DDL availability | `modify-column-reorg-transient-unknown-fatal` | `MODIFY COLUMN` rolls back on transient connection-family errors that sibling `ADD INDEX` retries through. | [draft](../bug-drafts/ai-native-modify-column-transient-rollback-draft.md) | [catalog](../core/ai-native-proof-obligation-catalog.md) |
| 1350002 | confirmed | DDL liveness | `dist-addindex-runtime-fundamental-retry-hang` | Distributed `ADD INDEX` hangs in running/retry on persistent `SetTSBeforeImportEngine` engine-not-found errors. | [draft](../bug-drafts/ai-native-dist-addindex-setts-engine-notfound-hang-draft.md) | [catalog](../core/ai-native-proof-obligation-catalog.md) |
| 1410001 | confirmed | DDL liveness | `dist-addindex-retryable-timeout-unbounded-loop` | Distributed `ADD INDEX` has no terminal retry budget for persistent `SetTSBeforeImportEngine` context-deadline-exceeded errors. | [catalog](../core/ai-native-proof-obligation-catalog.md) | [selector](../core/ai-native-selector-ledger.md) |
| 1440001 | confirmed | DDL availability | `async-commit-schema-change-safe-window-broken` | MDL-off `ADD INDEX` lets concurrent async-commit transaction fail despite `delayForAsyncCommit` safe-window protection. | [catalog](../core/ai-native-proof-obligation-catalog.md) | [selector](../core/ai-native-selector-ledger.md) |
| 1470001 | issue-filed | wrong-result | `addindex-downscale-drops-tail-worker-error` | common reorg `ADD INDEX` downscale can drop a removed tail worker error and publish an incomplete index. | [issue](https://github.com/pingcap/tidb/issues/69776), [draft](../bug-drafts/ai-native-add-index-downscale-error-drop-draft.md) | [root-cause ledger](../core/ai-native-root-cause-ledger.md) |
| 30001 | issue-filed | wrong-result | `partial-index-implication` | Planner can retain a partial index when the query predicate does not imply its predicate, silently omitting rows while `ADMIN CHECK TABLE` stays green. | [issue](https://github.com/pingcap/tidb/issues/69779), [draft](../bug-drafts/ai-native-partial-index-id30001-draft.md) | [method case](../method-cases/ai-native-id30001-method-case.md) |
| 1500002 | issue-filed | data-integrity | `flashback-fk-rebinds-recreated-parent` | `FLASHBACK TABLE` can publish existing orphan rows after same-name parent recreation. | [issue](https://github.com/pingcap/tidb/issues/69777), [draft](../bug-drafts/ai-native-fk-flashback-same-name-parent-rebind-draft.md) | [selector](../core/ai-native-selector-ledger.md) |
| 1500003 | issue-filed | data-integrity | `flashback-db-sequence-runtime-state-lost` | `FLASHBACK DATABASE` can move a recovered sequence backward and reuse IDs already present in recovered rows. | [issue](https://github.com/pingcap/tidb/issues/69781), [draft](../bug-drafts/ai-native-flashback-db-sequence-reset-draft.md) | [case](../method-cases/ai-native-flashback-db-sequence-reset-method-case.md) |
| 1590002 | confirmed | data-integrity | `importinto-data-before-index-finalization` | `IMPORT INTO ... FROM SELECT` can leave durable rows without secondary-index entries after index-engine finalization fails. | [draft](../bug-drafts/ai-native-import-into-partial-data-before-index-finalization-draft.md) | [root-cause ledger](../core/ai-native-root-cause-ledger.md) |
| 1620002 | confirmed | data-loss | `ttl-midjob-timezone-context-drift` | TTL can silently delete a refreshed `DATETIME` row when global `time_zone` changes during one job. | [draft](../bug-drafts/ai-native-ttl-midjob-timezone-drift-refreshed-row-draft.md) | [oracle](../core/ai-native-oracle-library.md) |
| 1650002 | confirmed | restore-safety | `br-abort-lock-suppresses-live-heartbeat` | BR abort can delete a live restore registry row because its row lock suppresses the heartbeat writer. | [draft](../bug-drafts/ai-native-br-abort-lock-suppresses-live-heartbeat-draft.md) | [oracle](../core/ai-native-oracle-library.md) |
| 1680003 | confirmed | backup correctness | `br-scheduler-removal-stale-error-false-success` | BR can report success with no backup artifact when PD scheduler removal fails. | [draft](../bug-drafts/ai-native-br-scheduler-removal-false-success-draft.md) | [selector](../core/ai-native-selector-ledger.md) |
| 1710003 | confirmed | DDL control plane | `resource-group-external-effect-before-ddl-commit` | Cancelled `ALTER RESOURCE GROUP` can leave the uncommitted runtime definition active in PD. | [draft](../bug-drafts/ai-native-resource-group-cancel-external-drift-draft.md) | [selector](../core/ai-native-selector-ledger.md) |
| 1740003 | confirmed | resource control | `runaway-watch-flush-error-drops-batch` | A transient watch-publication error can silently disable cross-node runaway quarantine. | [draft](../bug-drafts/ai-native-runaway-watch-flush-loss-draft.md) | [case](../method-cases/ai-native-id1740003-failed-publication-retry-owner-method-case.md) |
| 1770003 | confirmed | data-integrity | `importinto-processchunk-writer-close-false-success` | File `IMPORT INTO` can report success while publishing rows without secondary-index entries. | [draft](../bug-drafts/ai-native-import-writer-close-false-success-draft.md) | [case](../method-cases/ai-native-id1770003-deferred-terminal-error-method-case.md) |
| 1800003 | issue-filed | DDL control plane | `table-placement-pd-bundle-before-ddl-commit` | Cancelled table placement DDL can silently reduce the table's declared replica redundancy in PD. | [issue](https://github.com/pingcap/tidb/issues/69784), [draft](../bug-drafts/ai-native-table-placement-cancel-external-drift-draft.md) | [case](../method-cases/ai-native-id1800003-selector-reuse-method-case.md) |
| 1830003 | issue-filed | DDL availability | `tiflash-rule-delete-before-ddl-commit` | Cancelled TiFlash replica removal can leave stale available metadata and make TiFlash-only queries time out. | [issue](https://github.com/pingcap/tidb/issues/69785), [draft](../bug-drafts/ai-native-tiflash-replica-cancel-external-drift-draft.md) | [case](../method-cases/ai-native-id1830003-owner-consumer-oracle-method-case.md) |
| 1860003 | confirmed | disaster recovery | `crr-resume-state-unbound-lineage` | Reusing a CRR downstream bucket can make PITR trust a checkpoint from another upstream/task lineage. | [draft](../bug-drafts/ai-native-crr-resume-state-cross-lineage-draft.md) | [case](../method-cases/ai-native-id1860003-persisted-lineage-method-case.md) |
| 1890003 | confirmed | data loss | `lightning-importinto-finished-checkpoint-unbound-input` | A retained finished checkpoint can make a new nonempty Lightning input return success with no IMPORT job. | [draft](../bug-drafts/ai-native-lightning-importinto-finished-checkpoint-lineage-draft.md) | [case](../method-cases/ai-native-id1890003-input-lineage-method-case.md) |

## High-Severity Candidates / Legacy Queue

These rows are `severity=high` in `found_bug`, but still need stronger confirmation, issue filing, or re-triage before they should drive the main severe-hunting loop.

| ID | Status | Root cause ID | Why not mainline yet | Primary asset |
| --- | --- | --- | --- | --- |
| 30007 | candidate | `reorg-global-index-miss` | Candidate row; needs fresh reproduction and end-state oracle refresh before being treated as confirmed severe. | [draft](../bug-drafts/ai-native-reorg-global-index-reference-draft.md) |

## Known-Root Rediscoveries / Reusable Calibration

These rows are severe behaviors reproduced by the AI harness, but they match an existing upstream root and must not be counted or filed again as new bugs.

| ID | Status | Existing root | Reusable asset |
| --- | --- | --- | --- |
| 1530002 | known-duplicate | [TiDB #65958](https://github.com/pingcap/tidb/issues/65958) | [draft](../bug-drafts/ai-native-dist-addindex-local-engine-loss-crash-draft.md), [method case](../method-cases/ai-native-id1530002-method-case.md) |

## Reusable Lessons

- Wrong-result and published-inconsistent-index bugs are the highest-value validation targets because the oracle can be hard: `ADMIN CHECK TABLE`, table/index differential, exact witness row.
- DDL liveness bugs need a terminal oracle, not just a red error: distinguish `finite rollback`, `self-heal`, `unbounded running`, and `publish bad state`.
- Runtime-control actions such as `THREAD` downscale, owner handoff, pause/resume, and persistent retryable faults are first-class matrix dimensions.
- After a hit, immediately back-solve the selector and add a sibling/negative-boundary row so the next round does not overgeneralize.

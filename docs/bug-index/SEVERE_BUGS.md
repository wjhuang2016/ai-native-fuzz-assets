# Severe Bug Index

This index tracks the high-severity bug assets that should guide future mining.

Source of truth for status and severity is the remote `found_bug` table. This file is curated so each row also points to the most useful local asset in this repository.

Last verified: 2026-07-12

- Remote `found_bug`: `MAX(id)=1500002`, `COUNT(*)=89`, `COUNT(DISTINCT root_cause_id)=66`
- High-severity entries: 14

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

## High-Severity Candidates / Legacy Queue

These rows are `severity=high` in `found_bug`, but still need stronger confirmation, issue filing, or re-triage before they should drive the main severe-hunting loop.

| ID | Status | Root cause ID | Why not mainline yet | Primary asset |
| --- | --- | --- | --- | --- |
| 30001 | new | `partial-index-implication` | High impact if current, but status is still `new`; needs current-master confirmation and owner-facing issue quality. | [draft](../bug-drafts/ai-native-partial-index-id30001-draft.md) |
| 30007 | candidate | `reorg-global-index-miss` | Candidate row; needs fresh reproduction and end-state oracle refresh before being treated as confirmed severe. | [draft](../bug-drafts/ai-native-reorg-global-index-reference-draft.md) |
| 1500002 | candidate | `flashback-fk-rebinds-recreated-parent` | Current parent exists but recovered historical child rows are not revalidated against its rowset; distinct from id30016's missing-parent bypass. | [draft](../bug-drafts/ai-native-fk-flashback-same-name-parent-rebind-draft.md) |

## Reusable Lessons

- Wrong-result and published-inconsistent-index bugs are the highest-value validation targets because the oracle can be hard: `ADMIN CHECK TABLE`, table/index differential, exact witness row.
- DDL liveness bugs need a terminal oracle, not just a red error: distinguish `finite rollback`, `self-heal`, `unbounded running`, and `publish bad state`.
- Runtime-control actions such as `THREAD` downscale, owner handoff, pause/resume, and persistent retryable faults are first-class matrix dimensions.
- After a hit, immediately back-solve the selector and add a sibling/negative-boundary row so the next round does not overgeneralize.

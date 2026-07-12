# Root-Cause Ledger
> Started 2026-07-03. The headline metric for this method is **distinct root causes**, not
> `COUNT(found_bug)`. This ledger is the corrected scoreboard and the counting convention.

## Current remote snapshot (2026-07-12)

`found_bug`: 90 surfaces, 67 distinct root causes, 15 high-severity rows, 65 confirmed rows,
and 9 issue-filed rows. `id30001` is now `issue-filed/high` with upstream issue #69779; its
hint and no-hint observations remain one root, `partial-index-implication`.

## Counting convention

- A **surface** is one `found_bug` row / one draft / one repro. A **root cause** is one defect
  that one fix closes.
- Blast-radius siblings share their parent's root cause. Rule for "is this a new root?": the
  Reopen test in `ai-native-proof-obligation-methodology-v2.md` → Blast-Radius Stop Rule
  (fix-locus / new-reasoning / consequence-escalation). A different owner reached by the same
  fix is one root with a wider blast radius, not a new bug.
- **One main report per root cause.** Sibling surfaces are matrix rows in that report's
  appendix, not standalone reports. Record blast radius as "affects N owners", not N rows.
- Headline count = `COUNT(DISTINCT root_cause)`. Surface count is a secondary "blast-radius
  reach" figure, never the top-line number.
  (found_bug now has a groupable `root_cause_id` column, added and backfilled 2026-07-03; the
  headline query is `SELECT COUNT(DISTINCT root_cause_id) FROM found_bug`. The existing
  `root_cause` text column keeps the prose P/Q narrative and is unchanged. This ledger stays the
  human-readable map; the column is the machine-countable source of truth.)

## Corrected scoreboard (from found_bug, 2026-07-09)

Authoritative counts from the DB after backfilling `root_cause_id` (supersedes the earlier
~45% estimate, which over-merged the S3 extractor sub-shapes):

```text
72 confirmed surfaces  ->  50 distinct root causes   (surface inflation 31%)
by owner severity grade (surfaces / distinct roots):
  high 6 / 6    major 1 / 1    medium 45 / 32    low 20 / 15
```

Ten multi-surface roots absorb 32 surfaces (22 of them redundant); the top six absorb 24 — the
whole inflation lives here:

```text
s15-candidate-before-exists      6 surfaces -> 1 root  (630018/19/20/21/22, 1020001 create-user)
cache-folded-session-input       4 surfaces -> 1 root  (30034/35/36/37; 35 & 36 same var)
ddl-validation-metric-mismatch   4 surfaces -> 1 root  (630001/002/003/023)
ddl-dependency-gate-overbroad    3 surfaces -> 1 root  (630004/007/009)
exchange-idswap-orphan           4 surfaces -> 1 root  (30017/630014/630024/30039 analyze-options)
extractor-operator-arity         3 surfaces -> 1 root  (30030/31 ESCAPE, 33 match_type)
```

Two facts this makes undeniable. Severity is thin and clustered: only **6 roots are graded
high**, and **4 of those 6 are the same state-transforming-DDL-bypasses-an-invariant family** —
reorg-global-index-miss (30007), reorg-partition row-drop (600001), MODIFY-reorg CHECK bypass
(630013), EXCHANGE id-swap orphan (630014); the 5th is the partial-index planner bug (30001).
The 6th is the NT-DML stale split-range bug (1230001), which is the first high-severity txn/state
ingress hit from the improved loop.
And the low-severity mass (**20 surfaces / 15 roots**) came almost entirely from three selectors
(S15, S10, S11) that the old five-axis score over-rewarded because static-precheck targets are
cheap, precise, and easy-oracle.

## Incremental note (2026-07-11)

The table above is still the 2026-07-09 snapshot. The current remote `found_bug` state after
the 2026-07-11 inserts was:

```text
87 confirmed surfaces  ->  64 distinct root causes
```

Newest high-severity root added today:

- `id1290001 / addindex-fastreorg-pd-tso-retry-misclassified-fatal` (selector `S24`):
  fast-reorg `ADD INDEX` can hit `PD:client:ErrClientCreateTSOStream`, `err_count=1`, and
  immediate `rollback done` under short PD restarts in the active write-reorg window, while the
  sibling `fast_reorg=OFF` txn path is GREEN.
- `id1320001 / ddl-ingest-retryable-kv-family-misclassified-fatal` (selector `S24`):
  on testbed `8220955`, lower-bridge live `Ingest:NotLeader` / `KV:ErrNoLeader` inside
  `ingestctrl` stayed GREEN, but bridge-proximal live `ErrNoLeader` / `ErrKVNotLeader` /
  `ErrKVRegionNotFound` injections at `runIngestReorgJob` produced immediate `rollback done` on
  both `ADD INDEX` and `ADD PRIMARY KEY`, with same-environment GREEN controls. This is the
  strongest current example of "retryable foreign error loses retryability at the DDL classifier
  bridge".
- `id1350001 / modify-column-reorg-transient-unknown-fatal` (selector `S24`):
  on testbed `8220955`, bridge-level live `driver_bad_conn` / `net_conn_reset` / `grpc
  unavailable` split cleanly: sibling `ADD INDEX` jobs `1723/1761/1767` synced, while
  `MODIFY COLUMN` jobs `1726/1764/1770` all ended `rollback done`; `context_deadline_exceeded`
  was the shared GREEN control on both sides. This is the first confirmed non-index-family
  high-severity S24 root.
- `id1350002 / dist-addindex-runtime-fundamental-retry-hang` (selector `S25`):
  on testbed `8220955`, distributed `ADD INDEX` in the DXF ingest path stayed `running` in
  `write reorganization` when a source-native `SetTSBeforeImportEngine` `engine-not-found` error
  was injected persistently. The same-altitude one-shot control synced (`job 2322`), the held RED
  stayed live as `job 2319` / `global task 270003`, and clearing the fault let the job finish.
  This is a new high-severity DDL liveness root: a runtime fundamental error is misclassified into
  an idempotent rerun loop instead of failing or rolling back.
- `id1410001 / dist-addindex-retryable-timeout-unbounded-loop` (selector `S26`):
  on testbed `8220955`, the same distributed `ADD INDEX` / `SetTSBeforeImportEngine` lane split
  again on error semantics: one-shot `context_deadline_exceeded` stayed GREEN (`job 4002`,
  `task 300007 -> succeed`), but a persistent timeout kept `job 4007` / `task 300008` `running`
  for >90s and produced 247 repeated `meet retryable error` lines before fault removal. This is a
  different high-severity liveness root from `S25`: the timeout is plausibly retryable, but the
  outer DXF task layer has no terminal retry budget, so the DDL reruns forever instead of failing
  or reverting once the retry budget is exhausted.
- `id1440001 / async-commit-schema-change-safe-window-broken` (selector `S27`):
  on testbed `8220955` current-master front `14001`, a natural same-start `plain ADD INDEX +
  async commit + basic(insert -> update)` probe stayed RED with metadata lock disabled and GREEN
  with metadata lock enabled. The red arm returned `Error 8028 Information schema is changed`
  while the green arm preserved the exact final rowset under `ADMIN CHECK TABLE`. This is a
  different high-severity availability root from the retry families: a DDL path that explicitly
  claims a safe window for old-schema async commit no longer satisfies that contract at runtime.

The current best DDL availability/liveness portfolio now has four complementary roots:
`S24` proves "foreign transient faults never entered the retry budget", `S25` proves
"source-native runtime fundamentals were admitted into a retry loop that never should have
existed", `S26` proves "even genuinely retryable runtime timeouts can still wedge if the outer
DXF layer has no retry budget/escalation", and `S27` proves "a source-declared old-schema safe
window can rot into a natural `ErrInfoSchemaChanged` failure on current runtime." A full
severity-table refresh for the 126000x / 1290001 / 1320001 / 1350001 / 1350002 / 1410001 /
1440001 wave is still pending; treat the retroactive map below as the historical 2026-07-09
baseline plus the incremental notes above.

## Incremental note (2026-07-12)

After inserting `id1470001`, the remote `found_bug` state is:

```text
88 confirmed surfaces  ->  65 distinct root causes
```

Newest high-severity root:

- `id1470001 / addindex-downscale-drops-tail-worker-error` (selector `S28`):
  common-reorg `ADD INDEX` can publish an incomplete public index if a tail backfill worker
  produces a real post-batch error after `ADMIN ALTER DDL JOBS ... THREAD = 1` has canceled it.
  The live witness was `job 4452`: DDL reached `synced/public`, `ADMIN CHECK TABLE` reported
  `ERROR 8223`, and table-scan/index-scan counts split (`IGNORE INDEX=32768`, index/default
  path `30301`). The control without downscale rolled back on the same injected worker error.
  This is a high-severity wrong-result/data-consistency root, not a moderate wrong-error.

After inserting `id1500002`, the remote `found_bug` state is:

```text
89 surfaces  ->  66 distinct root causes
```

New high-severity candidate:

- `id1500002 / flashback-fk-rebinds-recreated-parent` (selector `S6`): `FLASHBACK TABLE`
  restores a historical child row against an empty same-name parent. The current parent exists,
  so a missing-parent fix would pass, and future DML still performs `Foreign_Key_Check`; however
  the already recovered row is an orphan and `ADMIN CHECK TABLE` is silent. A stronger run with
  `ON DELETE CASCADE` shows the same-name replacement can delete the recovered historical child row
  through a normal parent delete. The source and matrix support an independent fix locus: current-
  parent row membership or historical parent identity, not only current parent existence. Keep status
  `candidate` pending product/fix review.

## Retroactive root map

### Consequence 3 — data loss / corruption / bypass / liveness (the high-value lane)
| root cause | selector | surfaces | note |
| --- | --- | --- | --- |
| reorg-partition treats byte-equality as row identity, skips a row | S9 | id600001 | COUNT 2→1; the strongest hit |
| MODIFY COLUMN reorg writer bypasses existing CHECK | S17 | id630013 | raw `txn.Set`, no `CheckRowConstraint` |
| EXCHANGE PARTITION id-swap orphans side-state | S4 | id30017, id630014, id630024, id30039 | 4 owners (stats-lock / masking / TTL / analyze-options), one root |
| ADD COLUMN silently drops inline CHECK (accepts bad data) | S18 | id30032 | integrity loss, borderline C2/C3 |
| add-index MVI flattened-key owner mismatch wedges online DDL | S1 | id30038 | **liveness C3, upstream-verified 2026-07-04**: under sustained concurrent DML the ADD UNIQUE INDEX subjob loops on `invalid encoded key` (ErrCount climbing) stuck in write-reorg, only rolls back when DML pauses; also false-dup(1062). NOT silent data corruption. issue #69660. **First new C3 from the improved loop.** |
| distributed ADD INDEX retries runtime fundamental errors forever | S25 | id1350002 | persistent source-native `SetTSBeforeImportEngine` `engine-not-found` leaves the DXF task `running` until fault removal |
| distributed ADD INDEX has no retry budget for persistent retryable import timeouts | S26 | id1410001 | persistent `SetTSBeforeImportEngine` `context deadline exceeded` leaves the DXF task `running` and reruns it 247 times in 87s until fault removal |
| MDL-off safe window no longer protects old-schema async commit during online ADD INDEX | S27 | id1440001 | natural same-start `plain ADD INDEX + async commit + insert+update` returns `ErrInfoSchemaChanged` while MDL-on sibling is GREEN with exact-row oracle |
| common-reorg ADD INDEX downscale drops canceled tail-worker errors | S28 | id1470001 | `THREAD=1` downscale can cancel a busy tail worker; its post-batch error is dropped before result collection, so a partial index is published and `ADMIN CHECK TABLE` fails |
| FLASHBACK TABLE rebinds recovered FK rows to a recreated same-name parent | S6 | id1500002 (candidate) | current parent existence passes, but recovered child row membership is not revalidated; empty same-name parent leaves an orphan, and a same-key replacement with `ON DELETE CASCADE` can delete the recovered row while `ADMIN CHECK TABLE` stays silent |
| NT-DML stale tx_read_ts split range silently misses current rows | S23 | id1230001 | write reports success but derives BATCH ranges from stale snapshot; `1:110,2:20` vs current-rowset control `1:110,2:120` |

### Consequence 2 — wrong-result / wrong-acceptance / metadata
| root cause | selector | surfaces | note |
| --- | --- | --- | --- |
| extractor drops collation / value-normalization | S3 | id30010, id30018, id30019, id30013(cand) | valueToLower blast |
| extractor request-vs-render timezone split | S3 | id30012, id30023 | |
| extractor precision lowering | S3 | id30015 | |
| extractor interval→coarse-range skip | S3 | id30021 | |
| extractor backend-not-found ≠ SQL empty rowset | S3 | id30022, id30026 | |
| extractor cache-key granularity | S3 | id30027 | |
| extractor drops operator input (ESCAPE / match_type) | S3 | id30030, id30031, id30033 | 30030/31 same ESCAPE, 2 owners |
| partial-index implication proven wrong | planner | id30001 | genuine optimizer wrong-result |
| join_key_type_cast narrows numeric-string comparison domain | S20 | id30040 | INT/VARCHAR join misses scientific-notation match |
| RELEASE SAVEPOINT drops later savepoints | S21 | id1200002 | txn savepoint stack operation split; MySQL compatibility |
| plan cache reuses volatile scalar payload | S7 | id30020 | |
| plan cache omits consumed semantic switch | S7/S8 | id30024 | |
| plan cache coarse timezone-offset key | S7 | id30025 | |
| plan cache reuses folded implicit-session-input payload | S7 | id30034, id30035, id30036, id30037 | 4 surfaces, 1 root |
| prepared AST / validator semantic freeze | S8 | id30028, id30029(cand) | |
| FK MODIFY validator early-return on incomplete target | S16 | id630011, id630012 | 2 surfaces, one function |
| CREATE TABLE LIKE mutates source CHECK name | S13 | id630005 | metadata |
| CREATE TABLE LIKE copies source READ ONLY lock to target | S13 | id1200001 | target runtime-state clone |
| FLASHBACK restores duplicate CHECK name | S14 | id630006 | namespace |
| add-index rollback delete-range loses owner bit | S1 | id30009 | kv cleanup |
| reorg partition global index misses non-touched | S5 | id30007 | |
| FLASHBACK DATABASE dangling placement ref | S1/S6 | id30011 | |
| recover table skips FK parent-reference validator | S6 | id30016 | |

### Consequence 1 — wrong-error / non-idempotent
| root cause | selector | surfaces | note |
| --- | --- | --- | --- |
| candidate-validation before target-exists classifier | S15 | id630018, id630019, id630020, id630021, id630022, id102000 | 6 surfaces, 1 root — the worst offender |
| idempotence flag dropped in sibling executor branch | S15 | id630008 | |
| idempotence flag lost in spec split | S15 | id630010 | |
| raw-count precheck before existence classification | S15 | id630015 | |
| capability gate before existence classification | S15 | id630016 | |
| special-name classifier before missing-object catch | S15 | id630017 | |
| _(id30038 reclassified to Consequence 3 — liveness — after upstream verification; see C3 table)_ | S1 | id30038 | moved up: stuck online DDL under concurrent DML, not a mere wrong-error |
| dependency existence treated as semantic-change proof | S11 | id630004, id630007, id630009 | 3 surfaces, 1 root |
| validation-metric mismatch (byte vs char, transition allowlist) | S10 | id630001, id630002, id630003, id630023 | 4 surfaces, 1 root |
| internal validation SQL omits DEFAULT partition complement | S19 | id630025 | EXCHANGE PARTITION validation generates `not ()` for LIST DEFAULT |
| table-lock cross-schema rename stale key | S4 | id30008 | S4 born-from |
| sequence-default dangling reference | S2 | id30005 | boundary/candidate |

### Performance loop (separate ledger, not in the correctness count)
| root cause | selector | surfaces |
| --- | --- | --- |
| add-index pause/resume reworks completed backfill | PS1 | perf-30001 |
| TTL scan re-lock restarts from range start | PS1 | perf-30002 |
| ANALYZE interrupt loses sub-job progress | PS1 | perf-30003 / id30014 |

## What this changes going forward

## 2026-07-13 update: id1680003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| checked scheduler-removal error is replaced by stale setup error at terminal return | `CHECKED_ERROR_MUST_DOMINATE_TERMINAL_RESULT` | full/raw/txn/EBS backup and resolve KV data | external backup success with no backup artifact | confirmed high |

This is one root across five sibling sites. Count it once. The reusable boundary is not a missing
error check: the check exists, but the checked value does not dominate the public terminal owner.
Command status and required artifact must be observed together.

## 2026-07-13 update: id1710003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| PD resource-group mutation commits before cancellable DDL metadata publication | `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE` | ALTER RESOURCE GROUP | cancelled DDL changes live resource-control policy and splits metadata/runtime truth | confirmed high |

Count RU, priority, burst, and runaway-policy variants as one root. The ownership boundary is the
same: external PD state has no compensation when the local DDL commit owner aborts.

## 2026-07-13 update: id1740003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| failed runaway-watch publication discards the only retry payload | `FAILED_PUBLICATION_RETAINS_RETRY_OWNERSHIP` | KILL/COOLDOWN/SWITCH_GROUP watch publication | peer TiDB silently does not enforce quarantine | confirmed high |

Count policy actions and watch match types as one root. The failed batch owner and missing retry
edge are identical. Remote state after insert: 97 surfaces, 74 distinct root causes.

## 2026-07-13 update: id1770003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| deferred per-chunk writer Close errors are excluded from ProcessChunk's public result | `DEFERRED_TERMINAL_ERROR_DOMINATES_SUCCESS` | file IMPORT INTO local-sort data/index writers | finished import publishes rows without secondary-index entries | confirmed high |

Count data-writer, index-writer, disk, SST, and flush error variants as one root. This is distinct
from id1260008, where an already-visible data-writer error skips a sibling Close, and id1590002,
where a later engine error is visible after data publication. Remote state after insert: 98
surfaces, 75 distinct root causes, 23 high-severity rows.

## 2026-07-13 update: id1800003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| PD table-placement bundle commits before cancellable DDL metadata publication | `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE` | nonpartition ALTER TABLE PLACEMENT POLICY | cancelled DDL silently weakens the table's declared replica redundancy | issue-filed high, #69784 |

This reuses S35 but is a distinct product root from id1710003: it has a different DDL handler,
external API, durable object, compensation obligation, and user consequence. Count replica values,
policy names, and label constraints as blast radius. Remote state after insert: 99 surfaces, 76
distinct root causes, 24 high-severity rows.

## 2026-07-13 update: id1830003

| root cause | selector | surfaces | consequence | status |
| --- | --- | --- | --- | --- |
| TiFlash PD rule deletion commits before cancellable replica-metadata publication | `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE` | nonpartition SET TIFLASH REPLICA 0 | cancelled DDL leaves stale available metadata and TiFlash-only queries time out | issue-filed high, #69785 |

This is distinct from table placement and resource-group drift: the external rule group, metadata
shape, physical replica lifecycle, and runtime consumer differ. Count replica values, location
labels, and query shapes as blast radius. Remote state after insert: 100 surfaces, 77 distinct root
causes, 25 high-severity rows.

1. New hits are logged here by root cause first; a surface only gets its own row after passing
   the Reopen test. Blast-radius siblings append to an existing root as "affects +1 owner".
2. The target queue is ordered by `consequence` first (see Target Selection Rules). The
   consequence-1 selectors (S15, S10, S11) are capped: they reopen only on a new sub-shape or a
   consequence escalation, never on "another owner of the same shape".
3. The standing high-consequence lane — state-transforming DDL (reorg / backfill / id-swap /
   restore / pinned concurrent substate) bypassing a normal-path invariant — is sourced ahead
   of static-precheck targets. 4 of the 6 highest-severity roots are there, and S23 adds a
   non-DDL sibling: wrapper statements that clear one state input but internally re-enter the
   generic session state machine. The interleaving/state-ingress dimension has barely been run.

These are enforced by the loop, not just documented here: `root_cause_id` assignment via the
Reopen test is in the INTEGRATE contract, and consequence-first ordering + the wrong-error cap +
the high-consequence lane are in P4 of the scheduler — both in `ai-native-autonomous-loop.md`.

## 2026-07-13 update: id1620002

- Remote `found_bug`: 93 surfaces, 70 distinct root causes.
- New root: `ttl-midjob-timezone-context-drift`.
- Consequence: C3 direct silent data loss; a refreshed `DATETIME` row is deleted while the TTL job
  reports successful completion.
- Distinctness: #41043/#41044 covers time-zone change before job start and is GREEN on current
  source. id1620002 changes context between scan and delete inside one job; the missing owner is
  cross-phase interpretation context, not initial cutoff rendering.
- Counting rule: one root only. Offset direction, interval length, batch size, and DATE variants are
  blast radius, not additional bugs.

## 2026-07-13 update: id1650002

- Remote `found_bug`: 94 surfaces, 71 distinct root causes.
- New root: `br-abort-lock-suppresses-live-heartbeat`.
- Consequence: C3 direct coordination-state destruction; a live restore registry row is deleted.
  The direct caller then cleans checkpoints, but command-level cleanup was not executed in this
  harness and is not counted as observed data corruption.
- Distinctness: current-source P/Q/F generated the target; post-hit searches for the table, function,
  and restore-heartbeat-abort terms found no upstream issue.
- Counting rule: one root only. Heartbeat intervals, restore filters, and running/resetting status
  variants are blast radius.

# Root-Cause Ledger
> Started 2026-07-03. The headline metric for this method is **distinct root causes**, not
> `COUNT(found_bug)`. This ledger is the corrected scoreboard and the counting convention.

## Current remote snapshot (2026-07-26)

`found_bug`: 158 surfaces, 135 distinct recorded root IDs, 80 high-severity rows, and 140 confirmed
rows. The newest entry is `id3570003`: a failed multi-schema AUTO_RANDOM conversion can leave a
hybrid allocator identity, allowing a cold TiDB's generated `REPLACE` to overwrite an existing row.

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

## 2026-07-13 update: id1860003

- Remote `found_bug`: 101 surfaces, 78 distinct root causes, 26 high-severity rows.
- New root: `crr-resume-state-unbound-lineage`.
- Consequence: C3 direct restore-safety breach. CRR and PITR claim checkpoint 100 while the current
  upstream/storage lineage proves only 10; the fast path performs zero object checks.
- Distinctness: this is not checkpoint flush loss or stale heartbeat. The missing proof is semantic
  lineage identity on a successfully loaded persistent token.
- Counting rule: one root only. Task names, bucket schemes, checkpoint values, and cluster-replacement
  mechanisms are blast radius.

## 2026-07-13 update: id1890003

- Remote `found_bug`: 102 surfaces, 79 distinct root causes, 27 high-severity rows.
- New root: `lightning-importinto-finished-checkpoint-unbound-input`.
- Consequence: C3 direct silent lost import; public orchestration succeeds for nonempty current input
  while no IMPORT job is submitted.
- Distinctness: S39 is reused, but the owner and consumer differ from CRR: input file lineage and
  table-level job submission rather than upstream checkpoint and PITR restore boundary.
- Counting rule: one root only. File names, checkpoint drivers, keep modes, and table counts are
  blast radius.

## 2026-07-13 update: id1920003

- Remote `found_bug`: 103 surfaces, 80 distinct root causes, 28 high-severity rows.
- New root: `br-backup-checkpoint-unbound-source-cluster`.
- Consequence: C3 direct mixed-lineage backup artifact; the retry backupmeta publishes an old-cluster
  SST while the matching current-cluster range is omitted from work.
- Distinctness: S39 is reused, but the durable owner and highest consumer are BR source-cluster
  identity and final backupmeta, not CRR recoverable TS or Lightning job submission.
- Counting rule: one root only. PD address forms, storage backends, range boundaries, and replacement
  mechanisms are blast radius.

## 2026-07-13 update: id1950003

- Remote `found_bug`: 104 surfaces, 81 distinct root causes, 29 high-severity rows.
- New root: `lightning-checkpoint-unbound-target-table-generation`.
- Consequence: C3 direct silent lost import. A retained completed checkpoint lets classic Lightning
  exit 0 after same-name table recreation while the current nonempty input is absent from the new
  target generation.
- Distinctness: S39 is reused, but this is target-generation identity in classic Lightning, not
  importinto input lineage, CRR recoverable TS, or BR source-cluster artifact lineage.
- Counting rule: one root only. Checkpoint driver, table schema, row count, and recreation mechanism
  are blast radius.

## 2026-07-13 update: id1980003

- Remote `found_bug`: 105 surfaces, 82 distinct root causes, 30 high-severity rows.
- New root: `flashback-cluster-cache-side-state-exclusion`.
- Consequence: C3 direct write unavailability. `FLASHBACK CLUSTER` reports synced/public and reads
  still work, but cached-table DML fails before commit because its mandatory lock row is absent.
- Distinctness: the older table-cache drop-database finding leaves an advisory orphan row. This root
  restores a live capability bit while excluding a required owner, and reaches the DML commit path.
- Counting rule: INSERT/UPDATE/DELETE and lease values are blast radius. Reopen only for another
  restore-excluded owner with a distinct mandatory consumer.

## 2026-07-13 update: id2010003

- Remote `found_bug`: 106 surfaces, 83 distinct root causes, 31 high-severity rows at insertion.
- New root: `prepare-dedup-stale-derived-execution-context`.
- Consequence: C3 direct silent wrong result. A newly prepared ordinary SELECT reads an old snapshot
  after the session cleared `tidb_read_staleness`.
- Distinctness: payload identity is correct; the fast path overwrites fresh semantic analysis with a
  cached derived evaluator whose producer context is absent from the key.
- Counting rule: SQL forms and stale durations are blast radius. Reopen only for another derived
  owner with a different semantic producer or terminal consumer.

## 2026-07-13 update: id2040003

- Remote `found_bug`: 107 surfaces, 84 distinct root causes, 32 high-severity rows.
- New root: `distributed-backfill-partial-plan-on-tso-error`.
- Consequence: C3 direct silent index corruption. Distributed ADD INDEX reports `synced`, but the
  published index omits committed rows and `ADMIN CHECK TABLE` returns 8223.
- Distinctness: this is not merely a swallowed error. The retry closure owns a captured publishable
  slice, so error propagation alone produces duplicate failed-attempt residue.
- Counting rule: region counts, batch sizes, and transient TSO error forms are blast radius. Reopen
  only for a distinct retry payload owner or highest consumer.

## 2026-07-13 update: id2070003

- Remote `found_bug`: 108 surfaces, 85 distinct root causes, 33 high-severity rows.
- New root: `correlate-clone-breaks-active-access-path-alias`.
- Consequence: C3 direct silent wrong result. Alternative logical plans turn a nonempty aggregate
  IN subquery into `TableDual` and return an empty rowset.
- Distinctness: alternatives must not share mutable paths with each other, but canonical and active
  views inside one clone must share the corresponding cloned path. The violated identity is inside
  one clone, not cross-alternative sharing.
- Counting rule: aggregate choices, SQL forms, index layouts, and cost factors are blast radius.
  Reopen only for a different clone owner or canonical/active producer-consumer pair.

## 2026-07-13 update: id2100003

- Remote `found_bug`: 109 surfaces, 86 distinct root causes, 34 high-severity rows.
- New root: `pessimistic-retry-omits-user-var-side-effect-rollback`.
- Consequence: C3 direct silent wrong data. A concurrent unique-key insert should make UPDATE fail,
  but automatic retry changes the SETVAR-derived key, returns success, and commits another row image.
- Distinctness: this is default/recommended pessimistic statement retry with SETVAR inside the
  retried write, not deprecated optimistic whole-transaction replay of a read-only assignment.
- Counting rule: DML forms, user-variable types, expressions, indexes, and conflict timing are blast
  radius. Reopen only for a different retry owner or a different unrestored state owner.

## 2026-07-13 update: id2160003

- Remote `found_bug`: 111 surfaces, 88 distinct root causes after insertion.
- New root: `admin-cleanup-index-retry-state-survives-rollback`.
- Consequence: C2. Three dangling entries are repaired but reported as nine; more than the fixed
  20000-entry batch can panic after an internal transaction retry. No wrong durable index state was
  proved, so the row is moderate and excluded from the severe queue.
- Distinctness: this reuses S45 but has a different retry owner and missing state owner from
  id2100003. The survivor is executor batch/cursor state, not session user variables.
- Counting rule: entry counts, index definitions, and retryable error forms are blast radius. Reopen
  only if the same root reaches wrong durable data, or for another receiver-state owner and terminal
  consumer.

## 2026-07-13 update: id2190003

- Remote `found_bug`: 112 surfaces, 89 distinct root causes, 36 high-severity rows.
- New root: `pessimistic-retry-omits-last-insert-id-reset`.
- Consequence: C3 direct wrong durable data. A successful zero-match retry publishes 99 from its
  rolled-back attempt, and the next INSERT commits 99 instead of the statement-entry ID 7.
- Distinctness: this reuses the id2100003 retry owner but not its state owner or consumer. The
  survivor is `StatementContext.LastInsertID/LastInsertIDSet`; the successful attempt does not read
  it, and terminal statement publication exposes it after zero work.
- Counting rule: UPDATE forms, sleep windows, ID values, unique indexes, and gate predicates are
  blast radius. Reopen only for another omitted publication-state owner or a different retry owner.

## 2026-07-13 update: id2220003

- Remote `found_bug`: 113 surfaces, 90 distinct root causes, 36 high-severity rows, 98 confirmed rows.
- New root: `savepoint-omits-local-temp-table-dirty-size-restore`.
- Consequence: moderate wrong error. The table is visibly empty after rollback, but stale dirty-size
  accounting rejects a valid one-byte INSERT with error 1114.
- Distinctness: savepoint restores MemDB correctly. The missing owner is a mutable value behind a
  deliberately persistent map, not savepoint-stack semantics or retry residue.
- Counting rule: table schemas, byte limits, and payload sizes are blast radius. Reopen only for a
  different mutable owner or a higher correctness-bearing consumer.

## 2026-07-14 update: id2250003

- Remote `found_bug`: 114 surfaces, 91 distinct root causes, 37 high-severity rows, 99 confirmed
  rows.
- New root: `async-commit-age-check-after-recovery-proof`.
- Consequence: C3 terminal-truth contradiction. COMMIT returns an ordinary error, but real TiKV
  recovery can later commit the complete write set.
- Distinctness: the error occurs after the async recovery proof horizon; cleanup is only
  best-effort and does not dominate the independent recovery owner.
- Upstream: [TiDB #69831](https://github.com/pingcap/tidb/issues/69831), filed after revalidating the
  same RED on current client-go `01bd8f99`.
- Counting rule: age values, TTLs, cleanup failures, and key counts are blast radius. Reopen only
  for another post-proof terminal owner or outcome class.

## 2026-07-14 update: id2280003

- Remote `found_bug`: 115 surfaces, 92 distinct root causes, 38 high-severity rows, 100 confirmed
  rows.
- New root: `onepc-schema-check-before-prewrite-mdl-off`.
- Consequence: C3 direct persistent corruption. A successful 1PC INSERT ordered after ADD INDEX can
  omit the new index key; the TRUNCATE sibling can commit to the obsolete table identity.
- Distinctness: `id1440001` is async commit false abort under schema change. This root is 1PC false
  success because validation ends before TiKV's atomic apply; it needs a different fix owner.
- Counting rule: DDL types, delay sources, row counts, and index definitions are blast radius.
  Reopen only for a different validation horizon or irreversible semantic consumer.

## 2026-07-14 update: id2310003

- Remote `found_bug`: 116 surfaces, 93 distinct root causes, 39 high-severity rows.
- New root: `pessimistic-retry-retains-failed-attempt-advisory-lock`.
- Consequence: C3 liveness. A successful zero-row retry leaves an advisory lock owned only by its
  failed internal attempt and denies an independent session until release or disconnect.
- Distinctness: id2100003 affects UserVars consumed by re-entry; id2190003 affects terminal
  `LAST_INSERT_ID` publication. This root is an external lock capability whose own internal
  transaction remains live after statement completion.
- Frequency calibration: high consequence for singleton-job or distributed-lock users, but low
  trigger frequency because `GET_LOCK` must be evaluated inside retryable pessimistic DML.
- Counting rule: lock names, SQL forms, reference counts, retry counts, and delay windows are blast
  radius. Reopen only for another external capability owner or retry boundary.

## 2026-07-14 update: id2340003

- Remote `found_bug`: 117 surfaces, 94 distinct root causes, 40 high-severity rows, 102 confirmed
  rows.
- New root: `pipelined-commit-bypasses-undetermined-promotion`.
- Upstream: [TiDB #69821](https://github.com/pingcap/tidb/issues/69821), labeled
  `severity/critical`, `component/tikv-client`, and `found-by-ai`.
- Consequence: C3 critical data integrity. A successful primary Commit can be durable while
  pipelined DML returns an ordinary failure; the usable connection and ordinary class can induce a
  duplicate non-idempotent retry.
- Distinctness: id2250003 crosses the async recovery proof horizon and depends on failed cleanup.
  This root loses an already successful primary Commit response, records ambiguity correctly, but
  bypasses the canonical terminal promotion in a specialized finalizer.
- Frequency calibration: high catalog severity, critical consequence, lower frequency because
  `tidb_dml_type=BULK` is opt-in and the loss must land after primary apply.
- Counting rule: DML forms, transport errors, timeout lengths, key counts, and retry counts are
  blast radius. Reopen only for a different side-state producer or semantic finalizer.

## 2026-07-14 update: id2370003

- Remote `found_bug`: 118 surfaces, 95 distinct root causes, 41 high-severity rows, 103 confirmed
  rows.
- New root: `pessimistic-retry-reexecutes-setval-and-persists-null`.
- Upstream: [TiDB #69822](https://github.com/pingcap/tidb/issues/69822), labeled
  `severity/major`, `component/executor`, and `found-by-ai`.
- Consequence: C3 silent wrong data. A failed attempt changes a sequence owner; the hidden retry
  reads that state, changes `SETVAL` from 100 to NULL, and commits NULL into the row.
- Distinctness: earlier retry roots retain values or capabilities. This root is a cross-attempt
  write-read feedback edge where the failed attempt changes the successful attempt's output.
- Counting rule: sequence values, DML forms, sleep durations, and conflict shapes are blast radius.
  Reopen only for another state owner feeding a retry-visible correctness consumer.

## 2026-07-14 update: id2400003

- Remote `found_bug`: 119 surfaces, 96 distinct root causes, 42 high-severity rows, 104 confirmed
  rows.
- New root: `pessimistic-retry-advances-seeded-rand-output`.
- Upstream: [TiDB #69823](https://github.com/pingcap/tidb/issues/69823), labeled
  `severity/major`, `component/expression`, and `found-by-ai`.
- Consequence: C3 silent wrong data and terminal inversion. A hidden retry advances a constant-seed
  evaluator from its first deterministic value to its second, turning duplicate-key failure into
  successful commit of another key.
- Distinctness: the state owner is inside the prepared expression, not an external sequence,
  session field, or capability. `Clone` aliases the mutable RNG and retry construction occurs after
  the first attempt has already mutated it.
- Counting rule: seeds, thresholds, random-function variants, DML forms, sleep durations, and
  conflict shapes are blast radius. Reopen only for another mutable evaluator owner or retry
  boundary.

## 2026-07-14 update: id2430003

- Remote `found_bug`: 120 surfaces, 97 distinct root causes, 43 high-severity rows, 105 confirmed
  rows.
- New root: `pessimistic-retry-reuses-completed-cte-result`.
- Upstream: [TiDB #69826](https://github.com/pingcap/tidb/issues/69826), labeled
  `severity/major`, `component/executor`, and `found-by-ai`.
- Consequence: C3 silent wrong data. One successful row combines `u=2` from the retry snapshot with
  `v=10` from the failed attempt's completed CTE; one coherent execution persists `u=2,v=20`.
- Distinctness: #69823 retains mutable state inside an expression evaluator. This root retains a
  statement-owned materialized rowset whose `sync.Once` and completion marker suppress producer
  reconstruction.
- Counting rule: recursive/nonrecursive forms, consumer counts, CTE query shapes, DML verbs, delays,
  and conflict schedules are blast radius. Reopen only for another materialization owner or replay
  boundary.

## 2026-07-14 update: id2460003

- Remote `found_bug`: 121 surfaces, 98 distinct root causes, 44 high-severity rows, 106 confirmed
  rows.
- New root: `pessimistic-retry-publishes-failed-attempt-insert-id`.
- Upstream: [TiDB #69827](https://github.com/pingcap/tidb/issues/69827), labeled
  `severity/major`, `component/executor`, and `found-by-ai`.
- Consequence: C3 false terminal truth. A successful zero-row INSERT returns an explicit generated
  ID from a rolled-back attempt; applications can durably associate later data with that absent ID.
- Distinctness: #69796 owns `LastInsertID` and `LastInsertIDSet` mutated by
  `LAST_INSERT_ID(expr)`. This root owns singleton `InsertID` mutated by explicit nonzero
  auto-increment input. The #69796 field reset does not close this owner.
- Counting rule: explicit values, INSERT forms, sleep durations, and conflict schedules are blast
  radius. Reopen only for another terminal-output owner or replay boundary.

## 2026-07-14 update: id2490003

- Remote `found_bug`: 122 surfaces, 99 distinct root causes, 45 high-severity rows, 107 confirmed
  rows.
- New root: `fk-cascade-stmtcommit-drops-final-parent-lock`.
- Upstream: [TiDB #69828](https://github.com/pingcap/tidb/issues/69828), labeled
  `severity/critical`, `component/executor`, and `found-by-ai`.
- Consequence: C3 silent persistent relational corruption. Two pessimistic transactions both commit,
  but `ON UPDATE CASCADE` leaves a concurrently inserted child on the removed parent key; a fresh
  anti-join finds the orphan while `ADMIN CHECK TABLE` remains green.
- Distinctness: this is not a retry-state owner. An intermediate `StmtCommit` publishes enough state
  for the nested FK consumer while releasing the stage whose mutations must still participate in the
  outer pessimistic lock closure.
- Counting rule: FK actions, join shapes, child counts, isolation variants, and timing windows are
  blast radius. Reopen only for another intermediate publication owner or finalizer boundary.

## 2026-07-14 update: id2520003

- Remote `found_bug`: 123 surfaces, 100 distinct root causes, 46 high-severity rows, 108 confirmed
  rows.
- New root: `explain-analyze-dml-commit-before-kill-check`.
- Upstream: [TiDB #69829](https://github.com/pingcap/tidb/issues/69829), labeled
  `severity/critical`, `component/executor`, and `found-by-ai`.
- Consequence: C3 false terminal truth. `EXPLAIN ANALYZE UPDATE` can return error 1317 after its
  autocommit mutation is durable; retrying a non-idempotent update can apply it twice.
- Distinctness: #69821 bypasses undetermined promotion inside client-go's pipelined commit
  finalizer. This root commits in the TiDB session before a separate lazy RecordSet consumer checks
  SQLKiller and determines the public statement result.
- Frequency calibration: high catalog severity and critical terminal-integrity consequence, but a
  lower-frequency trigger because it needs diagnostic DML and a late cancellation window.
- Counting rule: DML verbs, explain formats, kill sources, timeouts, and timing windows are blast
  radius. Reopen only for another eager-effect owner or lazy terminal consumer.

## 2026-07-14 update: id2550003

- Remote `found_bug`: 124 surfaces, 101 distinct root causes, 47 high-severity rows, 109 confirmed
  rows.
- New root: `async-recovery-omits-failed-checknotexists-proof`.
- Consequence: C3 durable partial outcome. `COMMIT` returns duplicate, while later lock recovery
  commits an unrelated account/inventory/ledger mutation; application retry can apply it twice.
- Distinctness: #69831 crossed the async recovery frontier after all prewrites succeeded and then
  returned a late age error. Here one prewrite batch really fails, but its proof-only mutation was
  never represented in the recovery certificate.
- Frequency calibration: critical atomicity consequence; async commit is opt-in and cleanup must be
  interrupted, while lazy uniqueness, cross-Region tables, and ordinary prewrite ordering are natural.
- Counting rule: candidate schemas, values, business effects, Region layouts, and cleanup failures are
  blast radius. Reopen only for another proof class or certificate owner.

## 2026-07-14 update: id2580003

- Remote `found_bug`: 125 surfaces, 102 distinct root causes, 48 high-severity rows, 110 confirmed
  rows.
- New root: `commit-does-not-check-gc-safe-point-before-prewrite`.
- Upstream: [TiDB #69833](https://github.com/pingcap/tidb/issues/69833), labeled
  `severity/critical`, `component/tikv-client`, `sig/transaction`, and `found-by-ai`.
- Consequence: C3 silent durable row resurrection. An optimistic transaction whose start TS has
  stopped protecting the GC safe point can return COMMIT success and recreate a row that another
  transaction deleted before GC.
- Production trigger: `tidb_gc_max_wait_time` shorter than client-go's admission horizon, a stalled
  optimistic mutation, a conflicting transaction, and a normal GC/compaction cycle. `OFF` is needed
  only for the original existing-row UPDATE. Current-master real-TiKV expansion shows absent-row
  INSERT succeeds after insert-delete ABA under FAST and STRICT, and a STRICT child INSERT can commit
  after its validated parent is deleted, leaving a permanent orphan. No crash, DDL, async commit, or
  1PC is required.
- Distinctness: this is not a late commit error or recovery-certificate defect. GC has legitimately
  reclaimed the conflict evidence, but the retired start TS remains admitted to an effectful consumer.
- Counting rule: transaction durations, SQL verbs, row/FK proof representations, GC timing,
  assertion settings, and compaction schedules are blast radius. Reopen only for another retirement
  event or effectful consumer.

## 2026-07-14 update: id2610003

- Remote `found_bug`: 126 surfaces, 103 distinct root causes, 49 high-severity rows, 111 confirmed
  rows.
- New root: `commit-ts-expired-retry-bypasses-upper-bound-check`.
- Upstream: [TiDB #69836](https://github.com/pingcap/tidb/issues/69836), labeled
  `severity/critical`, `component/tikv-client`, `sig/transaction`, and `found-by-ai`.
- Consequence: C3 silent source/cache divergence that can become durable business-data corruption.
  SQL COMMIT succeeds with fresh source `v=1`, while a healthy TiDB's cache remains `v=0`; ordinary
  `INSERT SELECT` persists `0` into a regular sink table.
- Production trigger: cached table, ordinary optimistic 2PC, primary lock TTL longer than the fixed
  five-second WRITE lease, a writer-local pause longer than the lease, and a healthy peer read that
  pushes the live primary's `minCommitTS`. A roughly 4 MiB write gives about 12 seconds of lock TTL;
  a node-specific network interruption or long runtime/CPU scheduling pause supplies the writer gap.
- Distinctness: the initial commitTS is checked. The defect is that `CommitTsExpired` replaces that
  value with a new TSO and retries while inheriting a proof that was valid only for the old value.
  Related TiDB #36885 and client-go #564 do not close this replacement edge.
- Verification: local TiDB RED, owner-level client-go RED/GREEN, and full real-TiKV RED/GREEN are
  complete with MDL ON. The test observes natural `CheckTxnStatus`, minCommitTS push, and
  `CommitTsExpired`; injection only compresses the writer-local pause.
- Counting rule: row sizes, pause causes, cache values, SQL copy shapes, and timing schedules are
  blast radius. Reopen only for another proof owner, replacement site, or irreversible consumer.

## 2026-07-14 update: RC `UPDATE IGNORE` unique-key sibling (no new count)

- Current-source local and real-TiKV RED: with pessimistic READ COMMITTED, MDL ON, and non-default
  `tidb_rc_write_check_ts=ON`, session B deletes and commits the row owning unique value 20 after
  session A's prior statement. Session A's point `UPDATE IGNORE` still reports `ROW_COUNT()=0` and
  durably leaves `(1,10)` instead of claiming the now-free value as `(1,20)`.
- Owner: the old-TS classifier proves the outer PointGet row only. `DupKeyCheckInPlace` later reads
  the unique index through the transaction-owned snapshot, returns stale `ErrKeyExists`, and
  `IGNORE` removes the mutation that could otherwise produce a retrying conflict.
- Exact selector counterfactual: exclude `physicalop.Update{IgnoreError:true}` from old-TS reuse.
  The identical real-TiKV schedule becomes GREEN. Applying RCCheckTS to the whole transaction
  snapshot also fixes the cell but changes unrelated conflict-observation behavior and is too broad.
- Root accounting: this is a second consumer of the existing RC write-fast-path snapshot-owner split
  first validated by `INSERT IGNORE` + FK. It expands blast radius and births S58 resource closure,
  but does not change surface/root/bug counts or warrant a separate issue.
- Reachability calibration: `tidb_rc_write_check_ts` defaults to OFF, and the OFF control is GREEN.
  The reached consequence is silent lost write, but it is not a default-config critical finding.

## 2026-07-14 update: id2640003

- Remote `found_bug`: 127 surfaces, 104 distinct root causes, 50 high-severity rows, 112 confirmed
  rows.
- New root: `fk-cascade-savepoint-released-before-final-lock-result`.
- Upstream: [TiDB #69838](https://github.com/pingcap/tidb/issues/69838), labeled
  `severity/critical`, `component/executor`, `sig/transaction`, and `found-by-ai`.
- Consequence: C3 statement-atomicity violation. A pessimistic multi-table UPDATE returns definite
  error 1205; a later COMMIT of the still-open explicit transaction makes that failed statement's
  parent-key mutation and generated `ON UPDATE CASCADE` child mutation durable.
- Production trigger: a tenant/account primary-key migration includes a no-op migration-guard row as
  a database mutex. An older batch worker retains that row for more than the default 50-second lock
  timeout because of a long batch, hot Region, server-busy backoff, or storage pressure. The racing
  service catches 1205 as a retryable statement conflict and commits earlier audit/progress work.
- Verification: mock and real-TiKV RED, default-settings 50-second real-TiKV RED with MDL ON, and
  exact checkpoint-lifetime GREEN are complete. The fresh state is `2/2` on RED and `1/1` on GREEN.
- Distinctness: #69828 loses final lock ownership and permits two successful transactions to leave an
  FK orphan. This root loses rollback ownership after a definite statement error. Retaining the FK
  checkpoint through final locks closes this schedule without closing #69828's union-lock-set defect.
- Counting rule: timeout values, guard rows, parent/child schemas, migration identifiers, and lock
  producers are blast radius. Reopen only for another checkpoint owner, release boundary, or later
  fallible highest consumer.

## 2026-07-14 update: id2670003

- Remote `found_bug`: 128 surfaces, 105 distinct root causes, 51 high-severity rows, 113 confirmed
  rows.
- New root: `retry-autoid-positional-cache-overwrites-current-explicit-id`.
- Upstream: [TiDB #69845](https://github.com/pingcap/tidb/issues/69845), labeled
  `severity/critical`, `component/executor`, `sig/transaction`, and `found-by-ai`.
- Consequence: C3 silent persistent row-identity corruption. A successful autocommit
  `INSERT ... SELECT` retry combines explicit ID `100` from the failed attempt with payload `new`
  read by the successful attempt, while the current source mapping is ID `200`.
- Production trigger: a migration/reconciliation batch copies explicit external IDs from stable
  staging slots into an auto-increment target. A normal incremental publisher corrects one slot's
  mapping and updates another hot target entity while the batch scans. The hot-row prewrite conflict
  triggers transparent retry; no node failure, failpoint, DDL, or non-default SQL variable is needed.
- Verification: real TiKV records `9007`, `Exec_retry_count=1`, `Succ=true`, default MDL ON, and
  fresh target `100/new` versus source `200/new`. Current-datum classification before positional
  cache reuse retains the same retry and produces `200/new`.
- Distinctness: #20629/#20659 closes generated-ID buffer exhaustion by allocating after the buffer
  empties. This root is not exhaustion or an error; explicit business IDs share that buffer and are
  rebound by ordinal to a changed retry row.
- Counting rule: external IDs, staging schemas, DML syntax, hot-row choice, scan delays, and conflict
  timing are blast radius. Reopen only for another retry cache, provenance class, or logical-owner
  binding.

## 2026-07-14 update: id2700003

- Remote `found_bug`: 129 surfaces, 106 distinct root causes, 52 high-severity rows, 114 confirmed
  rows.
- New root: `server-info-restart-publishes-live-session-before-registration`.
- Consequence: C3 silent persistent row/index divergence. `ALTER TABLE ... ADD INDEX` and an old
  transaction both return success, but a fresh table scan is `[[1,10]]`, a forced new-index scan is
  empty, and `ADMIN CHECK TABLE` returns 8223.
- Production trigger: one TiDB's server-info session ends while its schema-sync session remains
  live; replacement lease creation succeeds, all five membership `Put` retries fail during a short
  control-plane recovery flap, and that unpublished replacement lease remains live through DDL and
  old-transaction COMMIT. Classic defaults and MDL ON are retained.
- Reachability boundary: a whole-process 95-second stall is GREEN because schema-sync restart makes
  old COMMIT return 8028. The severe surface requires independent server-info lease loss; this is a
  hard condition, not omitted detail.
- Exact owner GREEN: on publication failure, restore the completed prior session and close the
  unpublished replacement. The loop retries, membership returns, DDL waits for old COMMIT, and
  table/index finish `1/1` with green `ADMIN CHECK`.
- Distinctness: S37 previously covered failed publication followed by destruction of the retry
  payload. This root installs a live replacement owner before publication; its liveness suppresses
  the retry while the shared payload is absent.
- Counting rule: DDL verbs, table/index shapes, transaction modes, Put error strings, retry delays,
  and lease IDs are blast radius. Reopen only for another publication owner or highest consumer.

## 2026-07-16 update: id2730003

- Remote `found_bug`: 130 surfaces, 107 distinct root causes, 53 high-severity rows, 115 confirmed
  rows.
- New root: `pessimistic-retry-reuses-preprocessed-scalar-constant`.
- Consequence: C3 silent logical corruption. A pessimistic RC UPDATE and COMMIT both return success;
  fresh rows carry route `300` from the retry generation but aggregate `30` from the failed attempt.
  The coherent current-generation aggregate is `1029`; `ADMIN CHECK TABLE` remains green.
- Production trigger: a route/allocation batch stores a scalar ledger, inventory, or balance
  aggregate while joining a configuration table. A concurrent allocator claims the first route,
  inserts a value included by the aggregate, and advances configuration. Normal scan or storage
  latency lets the commit precede final pessimistic locking, naturally triggering transparent retry.
- Settings: MDL ON, default pessimistic mode, and default max retry count 256. READ COMMITTED is a
  common but non-default isolation setting. No failpoint, node failure, DDL, or TiKV tuning is needed.
- Verification: local `Exec_retry_count=1` RED, exact plan-rebuild GREEN, real-TiKV RED on TiDB
  `d573e28` plus three TiKV `67fccdb`, and a real-TiKV zero-retry allowed-outcome control.
- Distinctness: #69826 retains completed materialized CTE storage through `CTEStorageMap` and
  `sync.Once`. This root embeds a normal scalar result directly in `ExecStmt.Plan` as an
  `expression.Constant`; resetting CTE storage cannot close it.
- Counting rule: scalar functions, aggregate forms, DML verbs, values, delays, and conflict keys are
  blast radius. Reopen only for another preprocessed data owner, generation boundary, or irreversible
  consumer.

## 2026-07-17 candidate update: id2790003

- Remote `found_bug`: 132 surfaces, 109 distinct recorded root IDs, 54 high-severity rows, 116 confirmed
  rows.
- Candidate root: `snapshot-cleanup-tombstones-excluded-from-cross-cf-gc`; do not count it as a
  confirmed severe discovery yet.
- Consequence: persistent physical MVCC corruption. Snapshot apply first restores a readable long
  value; later lower-level GC leaves its Write record but permanently deletes the Default value, so
  a fresh read returns `DefaultNotFound`.
- Reachability gap: the probe makes exact `Write@21(start_ts=20)` live again while higher local
  versions remain below newer cleanup entries. Ordinary state-forward Raft snapshot application does
  not establish that rollback-shaped history; peer remove/re-add alone is not a sufficient trigger.
- Settings: default `raftstore.use-delete-range=false`, default
  `gc.enable-compaction-filter=true`, and a long value stored in Default CF. MDL is unrelated.
- Mechanism verification: current TiKV master `67fccdb16`, production cleanup API, complete-reapply pre-read,
  real RocksDB lower-level RED, physical `Write=true/Default=false`, exact `DefaultNotFound`, and
  full-input GREEN.
- Distinctness: #13448 covers flashback/reset visibility; #18081/#18096 covers concurrent ingestion
  latch ownership. Neither closes a post-snapshot lower-level compaction whose cross-CF effect uses
  only subset-local proof.
- Promotion gate: reproduce the same identity collision through a legal cluster recovery/rollback
  lifecycle. Keys, levels, safe points, and value sizes are not substitutes for this proof.

## 2026-07-25 update: id2850003

- Remote `found_bug`: 134 surfaces, 111 distinct root causes, 56 high-severity rows, and 118
  confirmed rows.
- New root: `importinto-nextgen-stale-target-generation`.
- Consequence: C3 direct full-input data loss from the live SQL object. The import job reports
  `finished` with row count `2`, while the current target contains neither row. Both record keys are
  written under the table ID retired by `TRUNCATE`.
- Production trigger: a detached NextGen import waits for a CSE worker during autoscaling,
  backpressure, restart, or temporary unavailability. A staging or maintenance workflow truncates
  the target before the worker starts. Worker recovery executes the persisted stale plan.
- Settings: MDL enabled, ordinary `TRUNCATE TABLE`, no product failpoint, no TiDB restart, and no
  transaction tuning. The test pauses only the downstream worker to make a legal queue delay
  deterministic.
- Verification: matched no-DDL GREEN, scheduler stale-owner RED, real CSE/TiKV name-swap diagnostic
  RED, and real CSE/TiKV `TRUNCATE` RED. The strongest run compares job status, row count, live table
  ID/rows, and raw record keys under the retired ID.
- Distinctness: Lightning checkpoint generation bugs authorize stale skip decisions. This root
  carries live import execution authority across user-job, SYSTEM-task, scheduler, and worker owners
  without a generation fence.
- Counting rule: DDL verbs, file formats, queue delays, row counts, and target names are blast
  radius. Reopen only for a different persisted identity owner, generation fence, or irreversible
  consumer.

## 2026-07-25 update: id2880003

- Remote `found_bug`: 135 surfaces, 112 distinct root causes, 57 high-severity rows, and 119
  confirmed rows.
- New root: `br-snapshot-restore-missing-target-write-fence`.
- Consequence: C3 silent persistent row/index corruption. BR reports `Table Restore success`, but
  a stale unique key returns a row whose projected predicate is false; primary and index counts
  differ, and `ADMIN CHECK TABLE` returns 8223.
- Production trigger: a long table restore exposes its target as `Normal`; ordinary application
  DML writes a backup primary key with a different unique value before SST ingest. Large data,
  slow object storage, resource pressure, or a restore rate limit supplies the window.
- Settings: official unmodified BR nightly, Classic real TiKV, MDL ON, default checksum OFF. The
  strongest RED uses no source patch, failpoint, process pause, node failure, or concurrent DDL.
- Verification: primary `128000/8192064000`, unique index `128001/8192064001`, wrong point lookup
  through old `u=100001`, raw `128000` record versus `256001` table keys, and matched no-DML GREEN.
- Distinctness: id2850003 retires a target generation across async IMPORT INTO owners. This root
  keeps one live table ID and corrupts its row/index bijection by overlapping logical DML with
  backdated physical restore.
- Counting rule: row values, keys, indexes, data size, rate limit, and DML verbs are blast radius.
  Reopen only for another write fence, timestamp domain, or durable physical consumer.

## 2026-07-25 update: id2910003

- Remote `found_bug`: 136 surfaces, 113 distinct root causes, 58 high-severity rows, and 120
  confirmed rows.
- Regression root: `cross-schema-rename-autoid-owner-not-reloaded`.
- Consequence: after cross-database `RENAME TABLE`, a cold TiDB reconstructs the allocator from the
  current schema instead of the persisted owner. With `AUTO_ID_CACHE=1`, `REPLACE` generates an
  existing ID, returns success, and silently overwrites its old row.
- Production trigger: an ordinary schema migration followed by scale-out, restart, rolling
  deployment, or routing to a cold peer.
- Verification: current nightly RED across PK insert, nonunique insert, and successful REPLACE data
  loss; exact current-source owner-resolution counterfactual GREEN on the same real TiKV.
- Distinctness: post-RED history found closed #55846/#55847, so this is recorded as a current-master
  regression with a stronger successful data-loss consumer.
- Counting rule: schemas, peers versus restarts, row counts, and SQL spellings are blast radius.

## 2026-07-25 update: id2940003

- Remote `found_bug`: 137 surfaces, 114 distinct root causes, 59 high-severity rows, and 121
  confirmed rows.
- New root: `import-target-active-owner-check-then-create-race`.
- Consequence: C3 persistent relational corruption. Two accepted import jobs can leave one record
  generation and both unique-index generations. On Classic, one job can report `finished`; point
  lookup for the losing input returns the winner row, and `ADMIN CHECK TABLE` returns 8223.
- Production trigger: two operators, pipeline workers, or an orchestrator retry submit detached
  imports into the same empty target at nearly the same time. The files can contain disjoint values;
  the collision is in independently generated hidden handles.
- Settings: Classic default import speed on one TiDB/PD/real TiKV and NextGen user/SYSTEM keyspaces
  with a CSE worker; MDL ON. The strongest runs used no product source modification, failpoint,
  process pause, node failure, or network/disk fault.
- Verification: NextGen natural concurrency RED on three consecutive first attempts. Classic
  default-config 100K and 1M overlapping runs were RED; the 1M run ended with 1M records and 2M
  unique-index entries. A sequential second Classic request was rejected with 8258 and the
  single-owner result was GREEN. A Classic run where DXF serialized both admitted jobs also stayed
  GREEN, isolating overlap across physical ingest as the irreversible boundary.
- Distinctness: id1590002 is one importer's partial data/index finalization; id2850003 is stale
  target generation; id2880003 is BR/live-DML write fencing. This root is a missing atomic
  singleton-owner claim that admits two healthy import jobs to one live target.
- Counting rule: request counts, files, values, index shapes, object stores, and timing widths are
  blast radius. Reopen only for another atomic claim, generated namespace, or durable consumer.

## 2026-07-25 update: id2970003

- Remote `found_bug`: 138 surfaces, 115 distinct root causes, 60 high-severity rows, and 122
  confirmed rows.
- New root: `autoid-to-autorandom-migration-reads-wrong-allocator-owner`.
- Consequence: C3 successful persistent data loss. After conversion, generated `REPLACE` statements
  can reuse existing primary keys, return `affected_rows=2`, and permanently remove the old rows
  while `ADMIN CHECK TABLE` remains green.
- Production trigger: a populated clustered `AUTO_INCREMENT` table uses `AUTO_ID_CACHE=1`; an
  operator enables the guarded supported conversion and changes the column to `AUTO_RANDOM`; the
  application resumes generated writes.
- Settings: Classic real TiKV, MDL ON, no concurrency, failpoint, process pause, node failure,
  retry, restart, or nondefault isolation. `AUTO_ID_CACHE=1` and
  `tidb_allow_remove_auto_inc=1` for the conversion are required.
- Verification: unmodified current master with its DDL ownership explicitly verified was RED;
  packaged nightly was RED; default-cache control was GREEN; selecting the separated
  `IncrementID` owner in `applyNewAutoRandomBits` was GREEN.
- Distinctness: id2910003 loses the persisted schema owner during cold reconstruction after rename.
  This root transfers state between allocator types in one column migration and selects the wrong
  old accessor.
- Counting rule: cache values, shard bits, row counts, and generated-write spellings are blast
  radius. Reopen only for another semantic owner, transfer primitive, or higher irreversible
  consumer.

## 2026-07-25 update: id3000003

- Remote `found_bug`: 139 surfaces, 116 distinct root causes, 61 high-severity rows, and 123
  confirmed rows.
- New root: `drop-table-fk-future-sibling-admission`.
- Consequence: C3 persistent relational corruption. Both DDL statements return success, but the
  renamed child survives without its parent; ordinary writes create orphan rows that persist after
  same-name parent recreation while `ADMIN CHECK TABLE` remains green.
- Production trigger: one cleanup or migration tool issues a parent-first multi-object
  `DROP TABLE IF EXISTS`; another deployment renames the future child after the parent job commits.
  Large object lists and ordinary DDL latency widen the window.
- Settings: unmodified current master and official nightly, Classic real TiKV, MDL ON, foreign-key
  checks ON, and no failpoint, source change, process pause, node failure, or transaction tuning.
- Verification: parent-first RED, child-first matched GREEN, fresh orphan anti-join, future valid
  and invalid write controls, source trace through full-list admission and per-object `doDDLJob2`.
- Distinctness: id2820003 freezes a reference graph inside multi-table rename; id1500002 rebinds a
  flashed-back child to a same-name parent; id2490003 loses rollback closure during FK cascade.
- Counting rule: table names, filler counts, row values, rename destinations, and `IF EXISTS`
  warning text are blast radius. Reopen only for another batch admission primitive, identity
  boundary, or higher irreversible consumer.

## 2026-07-25 update: id3030003

- Remote `found_bug`: 140 surfaces, 117 distinct root causes, 62 high-severity rows, and 124
  confirmed rows.
- New root: `pitr-autoid-required-repair-fail-open`.
- Consequence: successful persistent data loss. A final per-table rebase error is logged and
  swallowed; generated `REPLACE` can reuse a restored primary key and remove its preimage.
- Production trigger: point restore of an `AUTO_ID_CACHE=1` table, one transient metadata or autoid
  owner error during final repair, traffic resumption before allocator refresh, and a destructive
  generated-ID upsert.
- Verification: existing `pkg/kv/mockCommitErrorInNewTxn` RED, exact no-error GREEN, real SQL
  `ROW_COUNT`/`LAST_INSERT_ID`, fresh preimage reads, and source trace to BR's success terminal.
- Severity calibration: high/major. The durable consumer is critical-class, but the conjunction of
  PiTR, a nondefault table option, a repair-time failure, and `REPLACE` is too narrow for a critical
  production-frequency claim.
- Distinctness: #69485 owns the unconditional stale allocator before the repair existed. This root
  owns the repair's fail-open error contract on current master.
- Counting rule: error codes, table counts, ID values, and destructive upsert spellings are blast
  radius. Reopen only for another required invariant, repair owner, success terminal, or a
  materially more common trigger.

## id3450003 - BR publishes a PiTR metadata reference before its target

- Root cause ID: `br-pitr-migration-reference-before-extbackupmeta`.
- Module: BR snapshot restore with log backup enabled.
- Proof gap: appending `metaPath` to the durable migration is treated as proof that the
  `extbackupmeta` target exists, although the target is written afterward by a different storage
  operation.
- RED: interrupt the first collector after migration publication and before target creation; a
  successful retry uses a fresh path, but `LoadIngestedSSTs` fails on the retained old path.
- GREEN: create the initial metadata object before appending the migration reference; the identical
  retry leaves one readable path and the real log-restore metadata consumer succeeds.
- Severity: high, with critical disaster-recovery impact. Confirmed reachability requires abrupt BR
  process exit in the two-write window, so trigger likelihood remains narrower than default SQL or
  configuration-only roots.
- Counting rule: storage backends, restore scopes, and process-exit causes are blast radius. Reopen
  only for another durable reference owner, retry identity rule, or highest consumer.

## id3510003 - Classic import acquires TableMode after its schema proof is stale

- Root cause ID: `classic-import-tablemode-stale-schema-claim`.
- Module: Classic `IMPORT INTO` admission and schema fencing.
- Proof gap: `Plan.TableInfo` is captured before unguarded file discovery and prechecks;
  `TableModeImport` later blocks future DDL but does not atomically compare the current schema with
  that captured proof state.
- RED: publish `ADD UNIQUE INDEX` in the capture-to-claim gap. Both operations and the import job
  succeed, records are present, the public unique index is empty, a duplicate write succeeds, and
  `ADMIN CHECK TABLE` reports 8223.
- GREEN: publish the same index before plan construction. Record/index membership closes, the
  duplicate write returns 1062, and `ADMIN CHECK TABLE` passes.
- Validation blind spot: default required checksum passes because a completely missing index group
  is the additive identity while the local expected state was encoded from the obsolete schema.
- Severity: high in the repository taxonomy, with critical persistent-corruption consequence under
  ordinary successful operations and default validation.
- Counting rule: index kinds, file formats, discovery sizes, and timing widths are blast radius.
  Reopen only for a different proof token, guard owner, or irreversible consumer.

## id3540003 - GC prepare transaction does not own its config reads and writes

- Root cause ID: `gc-prepare-transaction-session-mode-split`.
- Module: GC worker enable admission and transaction safe-point publication.
- Proof gap: `prepare` starts a transaction to serialize against `tidb_gc_enable=OFF`, while every
  helper creates an independent session. The transaction contains none of the reads or writes named
  by its safety comment.
- RED: OFF returns successfully with global value 0 while prepare is paused after reading ON.
  Prepare then advances and broadcasts a safe point beyond a real historical version; the exact
  old snapshot becomes unreadable while the latest value remains.
- Direct consumer RED: `FLASHBACK DATABASE` passes the old safe-point check, production GC loads
  and deletes five ranges, and flashback still publishes `public/synced`; 64 recovered rows become
  0 while `ADMIN CHECK TABLE` passes on the consistently empty record/index keyspaces.
- GREEN: same-session config access plus `BEGIN PESSIMISTIC` makes OFF wait until prepare commits.
  Same-session access with plain `BEGIN` remains RED. In the direct-consumer GREEN, flashback waits
  for prepare and is cancelled with 8055 before publication.
- History: PR #8282 introduced the transaction obligation. PR #14403 removed the shared worker
  session to fix a panic and split the lock domain.
- Severity: high, with critical direct recovery-data-loss consequence and low timing probability.
  The official DR workflow treats value 0 as confirmation that history is retained.
- Counting rule: safe-point distances, GC intervals, PD latency, and recovery consumers are blast
  radius. Reopen only for another transaction identity, mode, or external publication owner.

## id3570003 - failed composite AUTO_RANDOM migration leaves a split allocator identity

- Root cause ID: `multi-schema-autorandom-migration-before-parent-commit`.
- Module: multi-schema DDL, modify column, and auto-ID allocator reconstruction.
- Proof gap: the parent and proxy subjob remain revertible while
  `checkAndApplyAutoRandomBits` has already set `AutoRandomBits`, rebased AutoRandom, and deleted
  the RowID owner.
- RED: the composite ALTER returns 1062 and history says `rollback done`, but `SHOW CREATE` combines
  `AUTO_INCREMENT` and `AUTO_RANDOM`. A cold TiDB's generated INSERT collides at ID 1; generated
  `REPLACE` succeeds at ID 2 with `affected_rows=2` and removes the old payload.
- GREEN: failing unique-index rollback alone keeps RowID at 30001; successful conversion alone uses
  a sharded ID; rejecting conversion before destructive apply preserves the pure old schema and
  cold ID 30001.
- Settings: default auto-ID cache, MDL ON, current master and packaged nightly, one real TiKV, no
  failpoint or DDL race. The guarded supported conversion and a later cold TiDB are required.
- Severity: high in the catalog, with critical-class successful persistent data loss.
- Distinctness: id2970003 selects the wrong old owner during a successful
  `AUTO_ID_CACHE=1` conversion. This root performs an irreversible owner migration before a
  composite parent commits and cannot restore it after a sibling failure.
- Counting rule: shard bits, duplicate values, row counts, index names, and cold-node startup form
  are blast radius. Reopen only for another irreversible child effect, rollback owner, or more
  common production trigger.

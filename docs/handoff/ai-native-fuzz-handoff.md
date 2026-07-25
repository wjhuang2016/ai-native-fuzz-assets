# AI-Native Bug Discovery Framework — Session 交接文档
> 最后更新:2026-07-25。负责人 wjhuang2016。本文是项目单一事实源,给下一个 session 直接接手用。
> 更详细的逐 session 流水账见项目记忆 `~/.claude/projects/-Users-bba-pc-tidb/memory/ai-native-test-framework.md`。

---

**2026-07-17 纠正：`id2790003` 降为 `high/candidate/confirmed=0`。** 真实 RocksDB RED/GREEN 已证明
cross-CF 删除机制，但普通 peer remove/re-add 并不能证明测试所需的状态历史可达：RED 让已被本地更高版本
覆盖的同一个 `Write@21(start_ts=20)` 通过 snapshot 再次成为 live；普通 Raft snapshot 是 state-forward，
通常只会重放相同或更新的逻辑状态。除非找到合法的 rollback/recovery 时间线，否则不能把它算作默认生产
路径上的已确认严重 bug，也不能提交上游 issue。

机制本身仍是有效研究资产：Write compaction input 只看到 lower SST 中
`Delete@101 -> Put@21`，只能证明 Put 在该输入内 stale；代码却把它提升成“全局无 Write 再引用
Default@20”，直接向另一 CF 写 point delete。snapshot cleanup tombstone 和完整恢复的 `Write@21` 位于
未被选择的新层，Q 不成立。

当前 TiKV master `67fccdb16` 的真实 RocksDB 探针已把时序压到两格。先写 long Put@21、Put@81、
Delete@101 并下沉到 L5；再走生产同构的 `DeleteStrategy::DeleteByWriter + allow_write=true` cleanup，
完整重放 snapshot 的 Write/Default 后 fresh MVCC read 明确成功。RED 只选择旧 L5 Write SST 做 L5->L6
GC，结果 `Write@21=true, Default@20=false`，fresh read 返回 exact `DefaultNotFound`。GREEN 保持同数据、
safe point=120、target level=6 和 filter 不变，只让 compaction input 包含 cleanup/reapply L0，结果
`Write=true, Default=true` 且 fresh read 成功。

涉及的 storage 开关均为默认：`raftstore.use-delete-range=false`、
`gc.enable-compaction-filter=true`。long value 进入 Default CF，RocksDB 也确实可以选择不含 idle L0 的
L1+ compaction；测试把 ratio threshold 设为 0 只是为了确定性，bottommost compaction 在默认 ratio 下也
会运行 GC。真正未闭合的是同一 MVCC Write 身份为何能合法复活，而不是配置或 LSM scheduler。历史
#13448 明确提过 reset-to-version 同族问题，所以 reset 候选只作为 selector 校准，不新增计数；#18081/
#18096 只验证单次 ingest 与 filter 的 latch 互斥，没有 durable data oracle，也未覆盖 snapshot 完成后的
lower-level compaction。post-RED 未找到 exact issue。

远端 `found_bug id2790003/high/candidate` 已纠正为 `confirmed=0`，当前
`132 surfaces / 109 recorded root IDs / 54 high / 116 confirmed`。方法增量 S62
`SUBSET_READ_CROSS_CF_SIDE_EFFECT_CLOSURE`：凡代码读取 files/levels/shards/generations 的子集，却向
别的 CF/registry/artifact 做 delete/publish/reclaim/repair，必须把 P(子集内成立) 与 Q(全局成立) 分开，
第一矩阵只改变 input closure。资产入口：
`assets/store/txn-snapshot-cleanup-compaction-closure-results-20260717.jsonl`、
`docs/bug-drafts/ai-native-snapshot-cleanup-lower-level-compaction-draft.md`、
`docs/method-cases/ai-native-id2790003-subset-read-cross-cf-side-effect-closure.md`、
`scaffolds/tikv-tests/ai_native_snapshot_cleanup_compaction_filter_test.rs`。暂停本 root 的 key/value/level/
safe-point/peer-event 变体。promotion gate 是合法 cluster-level 时间线；否则将 S62 迁移到另一个
cross-owner maintenance side effect。

---

**2026-07-16 txn campaign 命中 `id2730003/high`：悲观 RC 透明重试会复用失败轮次规划期
scalar-subquery constant，并把旧 aggregate 与新 join source 静默提交。** 候选来自 current source 的
attempt-generation owner 分析，不是 PR review finding。普通非相关 scalar subquery 在
`expression_rewriter.go:1602-1629` 被 `EvalSubqueryFirstRow` 执行后直接写成带 `SubqueryRefID` 的
`expression.Constant`；`handlePessimisticLockError` 接受 retry 后刷新 RC statement TS，却只对原
`ExecStmt.Plan` 调 `buildExecutor`，不重新规划。

真实生产形态是 route/resource allocator 批量 UPDATE：一列通过 config join 选择 unique route，另一列
存 ledger/inventory/balance scalar aggregate。并发 allocator 占用旧 route 200、插入 aggregate 应包含的
999，并把 config 推进到 300；大扫描、hot/cold Region、storage backoff 或表达式工作让它在 A 的 scalar
预处理后、final LockKeys 前提交。旧 route 冲突自然触发 TiDB 支持的 retry。确定性 probe 里的 `SLEEP`
只压缩这个窗口，不是生产触发条件。

testbed 8196300 当前 TiDB `d573e284`、三 TiKV `67fccdb`，MDL=ON、默认 pessimistic、默认 retry 上限；
唯一常见非默认维度是 READ COMMITTED。真实 RED：UPDATE affected=2、COMMIT success、target 为
`(1,100,31),(2,300,32),(3,200,999)`，同成功轮次状态 one-shot control 为
`(1,100,1030),(2,300,1031),(3,200,999)`，ADMIN CHECK green。去掉 unique conflict 的真实 TiKV
零重试控制，在 publisher 已把 src 改成 300 后仍只得到 old scalar/old source `(2,200,32)`；publisher
在 statement TS 前则是 new/new。所以 mixed old/new 不属于 RC 的合法单轮集合。

local current-source 测试记录 `Exec_retry_count=1, plan_cnt=1` 并 RED；只在失败轮次 rollback 后
`RebuildPlan`，相同冲突保留 retry=1、`plan_cnt=2`，结果变成 new/new。相关文件在本地 `531e40c` 与
testbed `d573e28` 间无 diff。post-RED GitHub issue/PR 搜索无 exact root；#69826 是
`CTEStorageMap+sync.Once` 物化状态，修复 owner 不同。远端 `found_bug id2730003/high/confirmed` 已入库，
当前 `130 surfaces / 107 roots / 53 high / 115 confirmed`。

方法增量是 S61 `ATTEMPT_LOCAL_PREPROCESSED_CONSTANT_REUSE` + O66。之前 RR 下同类 old/new 候选被
正确判 INVALID，因为 RR snapshot + DML current read 单轮即可达到；这份 negative asset 本轮反而指出
该变化 isolation owner。RC 把 read/forUpdate 都绑定到 attempt-local statement TS，使合法集合收紧为
`{old/old,new/new}`。后续扫描 planning/rewrite 中执行数据读取并产出普通 constant/plan field 的位置，
再与刷新 TS/schema/policy/metadata generation 但复用 plan 的 retry owner 求交，不再枚举 scalar SQL
语法。资产入口：`assets/store/txn-pessimistic-retry-scalar-subquery-rc-results.jsonl`、
`docs/bug-drafts/ai-native-pessimistic-retry-scalar-subquery-constant-draft.md`、
`docs/method-cases/ai-native-id2730003-attempt-local-preprocessed-constant.md`、两份 Go probe 和 TiDB test
fixture。暂停本 root 的 aggregate/DML/value/delay/conflict-key 变体；下一轮迁移 S61 到另一个规划期
data owner 或 retry generation。

---

**2026-07-14 retry-cache provenance pass 命中新的 critical-consequence data-integrity root
`id2670003`: autocommit `INSERT ... SELECT` 透明重试会把失败轮次的显式业务 ID 与成功轮次的新 payload
拼成一条静默提交的数据。** 候选来自 current source 的 replay-cache owner 分析，不是 PR review 或
issue seed。`adjustAutoIncrementDatum` 在第一轮把显式 nonzero auto-increment 输入与真正生成的 ID 一起
放进 `RetryInfo.autoIncrementIDs`；重试时又在解析当前 datum 前按 ordinal 消费旧值。动态 source 同一
位置从 `100/old` 变为 `200/new` 后，第二轮因此实际写入 `100/new`。

真实生产形态是 migration/reconciliation batch 从稳定 staging slot 读取 `target_id,payload`，显式写入
auto-increment materialization；普通 incremental publisher 把某 slot 的外部 ID 映射从 100 修正为 200，
同时更新 batch 也覆盖的另一条 hot target entity。大扫描、表达式计算、正常 storage latency 或 backoff
让 batch 的旧 snapshot 留在 flight；publisher commit 后，batch 在 hot row prewrite 命中真实 9007，
TiDB 按 classic 默认自动重放 autocommit statement。无节点故障、failpoint、DDL 或非默认 SQL 参数。

真实 TiKV RED 明确断言 MDL=1、autocommit=1、tidb_retry_limit=10、
pessimistic-auto-commit=false；slow log 为 `Exec_retry_count=1`、`Succ=true`、
`IsExplicitTxn=false`，fresh source 为 `200/new`，target 为 `100/new`。完整合法单轮结果集合只有
`{100/old,200/new}`，所以这不是 same-final-state oracle 过强。精确 owner GREEN 在 cache reuse 前分类
当前输入，只对本轮确实需要生成的 ID 复用旧缓存；相同时序保留 retry=1，target 恢复 `200/new`。

post-RED 去重发现历史 #20629/#20659，但其义务是生成 ID buffer 耗尽后继续分配、避免报错；本 root
是 full buffer 中显式 ID 缺少 provenance/logical-owner binding，最终静默数据错配。新 selector
`RETRY_CACHE_PROVENANCE_AND_IDENTITY` 要求 replay cache 记录
`certificate(value,provenance,logical_owner,generation,predicate)`，不能只保存 scalar/ordinal。远端
`found_bug id2670003` 已 issue-filed/high；当前 128 surfaces、105 roots、51 high、113 confirmed。上游
[#69845](https://github.com/pingcap/tidb/issues/69845) 已带 `severity/critical`、`component/executor`、
`sig/transaction`、`found-by-ai`。资产入口：
`docs/method-cases/ai-native-id2670003-retry-cache-row-identity.md`、
`docs/github-issues/id2670003-autoid-retry-mixes-old-id-new-payload.md`、
`scaffolds/tidb-tests/ai_native_autoid_retry_mixed_row_test.go`、
`runs/autoid-retry-mixed-row-real-tikv-20260714/`。该 root 已 terminal；禁止枚举 ID、表名、SQL 形态、
delay 或 hot-row 变体。

---

**2026-07-14 rollback-checkpoint horizon pass 命中新的 critical-consequence statement-atomicity root
`id2640003`: 悲观 FK cascade UPDATE 返回 1205 后，同一显式事务后续 COMMIT 会把失败语句的 parent/child
修改持久化。** 候选来自 current source 的 rollback-owner 分析，不是 PR review 或 issue seed。FK cascade
为了让 nested executor 看见主语句修改，会先做 intermediate `StmtCommit`；`prepareFKCascadeContext` 为此
建立 transaction savepoint，但 `handleStmtForeignKeyTrigger` 在 cascade 本身成功后立即 release。外层
`handlePessimisticDML` 此后仍要执行 final `LockKeys`，该步骤返回 terminal error 时，generic
`StmtRollback` 已无法撤销前面跨 stage 发布的 parent/child 修改。

真实生产触发已写到业务层：租户、商户或账号 ID 迁移更新 parent primary key，并通过
`ON UPDATE CASCADE` 修改 subscription/settlement/routing child；同一 multi-table UPDATE 用
`migration_guard.version=migration_guard.version` 作为 database mutex。旧 migration/reconciliation
worker 因大批次、hot Region、TiKV server-busy backoff 或 storage pressure 持有 guard 超过默认 50s；
新 worker 在 parent/cascade 已发布后，final LockKeys 等 guard 超时并收到 1205。显式事务不会被 1205
自动结束；把该错误当成 retryable statement conflict、需要保留此前 audit/progress 的服务随后 COMMIT，
就会触发。任何总在 1205 后整事务 ROLLBACK 的服务是明确 non-trigger control。

mock 与 real-TiKV 1s 压缩 RED 均为 UPDATE error 1205 + COMMIT success + fresh `(parent,child)=(2,2)`；
默认参数 real-TiKV RED 明确断言 `MDL=1`、`innodb_lock_wait_timeout=50`、pessimistic in-place check=1、
FK=1，`LockKeys_time=50.0017s` 后仍得到 `(2,2)`，不是调短 timeout 制造的现象。精确 owner GREEN 只把
FK savepoint 保留到 final lock 成功，并在 post-trigger terminal lock error 回滚到该 checkpoint；相同
1205 后 fresh state 恢复为 `(1,1)`，mock/real TiKV 都通过。

该 root 与 #69828 不同：#69828 丢的是 final lock owner，两个事务都成功后留下 orphan；本 root 丢的是
rollback owner，用户收到 definite statement error 后数据仍能提交。新 selector 为
`ROLLBACK_CHECKPOINT_FALLIBILITY_HORIZON`：把 savepoint/checkpoint 记为
`protects(C,effects,until=public terminal boundary)`，release 前必须枚举后续所有 fallible lock、validation、
render、ack/response consumer。远端 `found_bug id2640003` 已 issue-filed/high；当前 127 surfaces、104 roots、
50 high、112 confirmed。上游 [#69838](https://github.com/pingcap/tidb/issues/69838) 带
`severity/critical`、`component/executor`、`sig/transaction`、`found-by-ai`。资产入口：
`docs/method-cases/ai-native-id2640003-rollback-checkpoint-fallibility-horizon.md`、
`docs/github-issues/id2640003-fk-cascade-final-lock-timeout-partial-commit.md`、
`scaffolds/tidb-tests/ai_native_fk_cascade_final_lock_timeout_test.go`、
`assets/store/logs/txn-fk-cascade-final-lock-timeout-real-tikv-defaults-red-20260714.log`。该 root 已 terminal；
禁止枚举 timeout、guard row、FK shape、identifier 或 lock-hold cause 变体。

---

**2026-07-14 fast-path resource-closure pass 验证了一个 severe sibling，但不新增 bug/root 计数：
悲观 RC `UPDATE IGNORE` 在并发事务已经释放 UNIQUE value 后，仍可能静默跳过合法更新。** 真实业务
场景是账号名、外部订单号等唯一业务标识回收：A 开启 pessimistic READ COMMITTED transaction，先读取
目标行 `(1,10)` 与旧 owner `(2,20)`；普通 cleanup 事务 B 删除 `(2,20)` 并提交；fresh observer 已确认
20 可用；A 再按主键执行 `UPDATE IGNORE ... SET u=20 WHERE id=1`。要求精确时序
`A.latestOracleTS < B.commitTS < A.UPDATE`，所有 TiDB/PD/TiKV 都健康、MDL default ON、无 failpoint。

当前源码在 `tidb_rc_write_check_ts=ON` 时把 Point Update 判为可复用旧 TSO，但这个 P 只覆盖目标 row。
`UPDATE IGNORE` 的 `DupKeyCheckInPlace` 还会通过 transaction snapshot 读取新 unique-index key；它看到
旧 index entry 后返回 `ErrKeyExists`，随后 IGNORE 跳过该 row。因为 mutation set 已空，后续 TiKV
prewrite/lock conflict 没有机会触发 retry。local 与 real-TiKV RED 都得到 SQL success、`ROW_COUNT()=0`、
fresh durable row 仍为 `(1,10,100)`；release-before-BEGIN 与 `tidb_rc_write_check_ts=OFF` 都 GREEN。只让
`physicalop.Update{IgnoreError:true}` 退出旧-TS selector 后，同一 real-TiKV schedule 变为 `(1,20,101)`。

必须披露 reachability：`tidb_rc_write_check_ts` 当前默认 OFF，所以它只影响显式开启 TSO 优化的生产
集群，不能包装成 default-config critical。根因级去重后，它与此前 RC `INSERT IGNORE` + FK helper 的
snapshot-owner split 相同，只是隐藏 consumer 从 FK parent read 扩到 unique-index duplicate read；因此
不新增 ID、不单独提 issue。方法论新增 S58 `FAST_PATH_EXECUTION_RESOURCE_CLOSURE`：看到 PointGet/
single-row/no-new-TS 证明后，必须列全 target row、unique index、FK、cascade、trigger、default/sequence
等实际资源，并为每个资源记录 snapshot owner、freshness/conflict proof 与 error policy。资产入口：
`docs/bug-drafts/ai-native-rc-update-ignore-freed-unique-sibling.md`、
`assets/store/txn-rc-update-ignore-freed-unique-results.jsonl`、
`assets/store/logs/txn-rc-update-ignore-freed-unique-20260714.log`。

---

**2026-07-14 value-replacement pass 命中新的 critical-consequence data-integrity root
`id2610003`: `CommitTsExpired` 重试可以绕过 cached-table commitTS 上界检查，在 WRITE lease 过期后
仍成功提交。** 候选完全来自 current source 的 proof owner 分析：TiDB 为 cached-table commit 获取
WRITE lease，并把 `commitTS < lease` 作为 checker 交给 client-go；普通 2PC 的初始 commitTS 会检查，
但 TiKV 返回 `CommitTsExpired` 后，client-go 在 `commit.go` 生成 replacement TSO 并直接重试，没有对
新值重跑 checker。代码证明的是 `P(commitTS1)`，却把它当成了 `P(commitTS2)`。

真实生产触发不是泛泛的“in-flight error”，而是一条具体时序：业务显式启用 table cache；一次较大
optimistic write 使 primary lock TTL 长于固定 5s WRITE lease（约 4 MiB 写入在当前公式下约为 12s，
生产 cap 为 20s）；prewrite 和初始 checker 后，writer TiDB 因节点特有的 TiDB-to-TiKV/PD 网络中断、
长 stop-the-world pause、严重 CPU 饥饿或 OS/container 调度停顿而超过 5s 不再提交/续租，另一健康 TiDB
在 lease 过期后普通 SELECT，获取 READ lease 并由 TiKV `CheckTxnStatus` 推高仍存活 primary lock 的
`minCommitTS`；writer 恢复后首个 Commit 自然得到 `CommitTsExpired`，replacement commitTS 已越过旧
WRITE lease，却因漏检而第二次 Commit 成功。小事务约 3s TTL 会先被 reader 回滚，是明确负控制。

local 与 pinned real-TiKV RED 都观察到真实 prewrite、reader-driven minCommitTS push、首个
`CommitTsExpired`、两个 Commit RPC 和 SQL COMMIT success；post-commit cached SELECT 为 `v=0`，
NOCACHE source 为 `v=1`。更高消费者 `INSERT INTO sink SELECT ... FROM cached_table` 把旧值 0 持久化到
普通 sink；NOCACHE 后 source 保持 1、sink 保持 0。只在 replacement TSO 后重跑 existing checker 的
精确反事实使 checker 调用 1->2、Commit RPC 2->1、COMMIT fail，local/real TiKV 都 GREEN，且首个
TiKV `CommitTsExpired` 仍自然发生。MDL default ON，ordinary 2PC；1PC/async 因 checker 自动关闭。

新 selector `VALUE_REPLACEMENT_PROOF_REVALIDATION` 把 proof 存为带参数 token，例如
`checked(commitTS=x, lease=L)`，然后枚举 proof 后所有 value replacement 与最高 irreversible consumer；
每条边必须 revalidate、证明单调蕴含，或 fail closed。远端 `found_bug id2610003` 已 issue-filed/high；
当前 126 surfaces、103 roots、49 high、111 confirmed。post-RED 搜索只找到 related TiDB #36885、
client-go #564/#1316，它们没有关闭 replacement commitTS 的 lease proof，不是 exact duplicate。该 root
已通过 [#69836](https://github.com/pingcap/tidb/issues/69836) issue-filed，带 `severity/critical`、
`component/tikv-client`、`sig/transaction` 和 `found-by-ai`；禁止枚举 row size、pause cause、cache value、
SQL copy shape 或 timing 变体。

资产入口：`docs/bug-drafts/ai-native-cached-table-commit-ts-retry-draft.md`、
`docs/method-cases/ai-native-id2610003-value-replacement-proof-revalidation.md`、
`assets/store/logs/txn-cached-table-commit-ts-retry-real-tikv-20260714.log`、
`scaffolds/tidb-tests/ai_native_cached_table_commit_ts_retry_test.go`、
`scaffolds/client-go-tests/ai_native_commit_ts_upper_bound_retry_test.go`。下一轮回到不同 proof owner 的
value replacement，不继续 cached-table/CommitTsExpired 变体。

---

**2026-07-14 safe-point retirement pass 命中新的 critical data-integrity root `id2580003`:
过期 optimistic transaction 可以在 GC 后成功提交并复活已删除行。** 候选完全来自 current source
consumer closure:TiDB 会把超过可配置 `tidb_gc_max_wait_time` 的 active startTS 排除出 min-start-TS,
snapshot Get/BatchGet/Scan 都调用 `CheckVisibility`,但 effectful `KVTxn.Commit` 在 prewrite 前没有同类
admission。支持下限 600s 与 client-go 固定 24h age guard 形成真实窗口;TiKV compaction 一旦回收 newer
DELETE tombstone,stale prewrite 就失去 write-conflict 证据。

raw client-go 与 SQL-level real-TiKV RED 均得到 stale COMMIT success + fresh value/row resurrected;
SQL 格为 MDL default ON、1PC OFF、async OFF。第一轮在新装 Classic 的 FAST assertion 下被
`Assertion=Exist` 挡住,但这只是 mask:当前注册兼容默认仍是 OFF,FAST 只在 initial bootstrap 写入,
升级集群可能 retain/fallback OFF。真实生产触发为运维将 `tidb_gc_max_wait_time=600` 以避免卡住事务
阻塞 GC,业务 optimistic UPDATE 因外部调用/暂停挂住约 20-30 分钟,清理任务 DELETE 同行,normal GC 与
write-active compaction 回收历史,旧事务再于 24h 内恢复 COMMIT。确定性探针只压缩 GC phase 等待,
GC/compaction/prewrite/fresh read 都是真实路径。

精确反事实是在 nonempty commit 的 prewrite 前调用 `CheckVisibility(startTS)`:同格返回 9006 且行保持
deleted;这证明 admission owner,但完整修复还需关闭 check/prewrite TOCTOU。新 selector
`SAFE_POINT_RETIREMENT_CONSUMER_CLOSURE` 要求退休任何 timestamp/generation/owner 后枚举所有 effectful
consumer,并把 guard 分为 mandatory/new-install/upgrade-fallback/session-config/best-effort,避免把 mask
误判为 proof closure。远端 `found_bug id2580003` 已 issue-filed;当前 125 surfaces、102 roots、48 high、
110 confirmed。上游 [#69833](https://github.com/pingcap/tidb/issues/69833) 带
`severity/critical`、`component/tikv-client`、`sig/transaction`、`found-by-ai`。该 root 已 terminal;
禁止枚举 transaction duration、SQL shape、GC phase、assertion level 或 compaction timing 变体。

---

**2026-07-14 specialized-finalizer pass 命中新的 critical terminal-integrity root `id2520003`:
autocommit `EXPLAIN ANALYZE` DML 可以在 mutation 已提交后返回 1317 `Query execution was
interrupted`。** 这条由 current source 的 eager-effect/lazy-result split 独立产生,不是 PR review 或历史
issue seed。`ExecStmt.handleNoDelay` 先执行 inner DML,`session.ExecuteStmt` 在返回非空 explain
RecordSet 前调用 `StmtCommit + CommitTxn`;但第一次 `recordSet.Next` 仍会先消费 `SQLKiller` signal,再生成
explain chunk。因此 late `KILL QUERY` 可以成为 durable commit 之后的 definite statement error。

确定性 local RED 在 `ExecuteStmt` 返回 RecordSet 后、第一次 `Next` 前发送 production-equivalent
`SQLKiller.QueryInterrupted`:client terminal 为 error 1317,fresh observer 却读到 `v=1`;contract 期望
error 对应 `v=0`。同一 DML/同一 signal 放在 explicit pessimistic transaction 中,随后 rollback,fresh
observer 为 `v=0`,证明差异只来自 commit 是否越过 lazy terminal boundary。MDL 为默认 ON。post-RED
GitHub 搜索无 exact root;[#37373](https://github.com/pingcap/tidb/issues/37373) 是旧的执行行数问题,不是
本 root。

新 selector `IRREVERSIBLE_EFFECT_BEFORE_LAZY_TERMINAL_CHECK` 从 eager DML/external effect 返回的 lazy
RecordSet、iterator、stream、encoder、Close finalizer 出发,寻找位于第一次 fallible consumer 之前的
`Commit/Publish/external apply`;再在边界注入真实 cancellation/render/encode signal,用
`TERMINAL_ERROR_VS_FRESH_DURABLE_STATE` 判定。允许状态只有 success+effect、definite pre-commit
error+no effect,或显式 undetermined+possible effect。远端 `found_bug id2520003` 已入库;当前 123
surfaces、100 roots、46 high、108 confirmed。上游
[#69829](https://github.com/pingcap/tidb/issues/69829) 已带 `severity/critical`、`found-by-ai`、
`sig/transaction` 和 `component/executor`。资产入口:
`assets/store/explain-analyze-dml-post-commit-kill-results.jsonl`、
`docs/method-cases/ai-native-id2520003-lazy-terminal-after-commit.md`、
`docs/github-issues/id2520003-explain-analyze-dml-post-commit-kill.md`。该 owner 已 terminal;禁止枚举
DML verb、explain format、kill source、timeout 和 timing 变体。下一轮把 selector 移到更高频 ordinary
DML/RETURNING 或 import/stream lazy-terminal owner。

---

**2026-07-14 intermediate-publication pass 命中新的 critical consequence root `id2490003`:
悲观事务执行 `ON UPDATE CASCADE` 时,并发 child insert 与 parent-key update 可以同时 COMMIT 成功,
最终留下持久化 FK orphan。** 这条不是从 PR review 或历史 issue 出发。起点是一个新的证明义务:
内部 cascade 为了看到主 DML 会提前 `StmtCommit`,但这个中间发布不能丢掉外层语句最终需要持有的锁。

源码链路为 `handleStmtForeignKeyTrigger -> StmtCommit -> flushStmtBuf/Release stage -> cleanup/init fresh
stage -> handlePessimisticDML.KeysNeedToLock`。最后一步只 inspect 当前 fresh stage,已 release 的 old-parent
mutation 没有进入最终 pessimistic lock set。确定性 local RED 在该窗口让 competitor 插入 `(200,1)` 并
commit,owner 随后把 parent `1` 改为 `2` 并 commit;fresh observer 得到
`parent=[[2,10]],child=[[100,2],[200,1]],orphans=[[200,1]]`,且两张表的 `ADMIN CHECK TABLE` 都成功。
只在第一次 FK `StmtCommit` 前 acquire 当前 lock set 的 proof counterfactual 会让 competitor 等待并报
1452,anti-join 为空。MDL 为默认 ON。

新 selector `INTERMEDIATE_PUBLICATION_LOCK_CLOSURE` 枚举 `Publish/Release/Commit/Flush` 中间边界,
与后续仅扫描 current stage/generation 的 `Lock/Validate/Finalize` 相交,再减去显式 owner transfer、union
journal 和已持锁 cache。生成 schedule 时必须优先 disjoint physical key 上的 semantic invariant;同 key
冲突可能只得到 TiKV late error,掩盖高层锁义务缺失。强 oracle 为
`BOTH_COMMIT_SUCCESS_PLUS_FK_ANTI_JOIN`。远端 `found_bug id2490003` 已入库;当前 122 surfaces、99 roots、
45 high、107 confirmed。上游 [#69828](https://github.com/pingcap/tidb/issues/69828) 已带
`severity/critical`、`found-by-ai`、`sig/transaction` 和 `component/executor`。资产入口:
`assets/store/pessimistic-fk-cascade-orphan-results.jsonl`、
`docs/method-cases/ai-native-id2490003-intermediate-publication-lock-closure.md`、
`docs/bug-drafts/ai-native-pessimistic-fk-cascade-orphan-draft.md`。该 FK stage owner 已 terminal;禁止枚举
FK action、join、isolation 或 delay 变体。下一轮只复用 selector 到不同中间发布/最终闭包边界。

---

**2026-07-14 consumer-first terminal-output pass 命中新的 high correctness root `id2460003`:
悲观 RC retry 会把失败 attempt 的显式 auto-increment ID 放进成功语句的 MySQL OK packet。** 这次
不是从 PR review、issue 或历史修复出发,而是从公共终端消费者反向切片:server 写 OK packet 时调用
`session.LastInsertID()`,它会 fallback 到 `StmtCtx.InsertID`;显式非零 auto-increment 输入会在第一次
attempt 写这个 singleton field,但 `ResetForRetry()` 没有清理它。

自然冲突矩阵让第一 attempt 计算 `id=42` 后撞 unique key;竞争事务同时写入 gate,使成功 retry
插入 0 行。local RED 为 `retry=(affected=0,insert_id=42)`,同 final state direct control 为
`control=(0,0)`,sink 持久化为 `retry=42,control=0`;只加 `InsertID=0` 的精确反事实变为全绿且 retry
仍为 1。testbed `8220955` 的 `database/sql + go-sql-driver/mysql` real-TiKV lift 得到同样结果,slow
log 记录 `Exec_retry_count=1,Result_rows=0,Succ=true`,MDL default ON,无 failpoint,测试 schema 已删除。

新 selector `PROTOCOL_OUTPUT_RESET_DIFFERENTIAL` 不再假设状态必须是 value/flag 对:先枚举 OK packet、
错误、warning、row count、generated ID 等公共输出,反向切到 mutable owner,与 retry 前可变字段相交,
再减掉 reset 覆盖和成功 attempt 必然覆写项,最后用 zero-work re-entry 暴露 survivor。它修复了旧
`value + Set/Valid/Dirty/...` scanner 漏掉 singleton `InsertID` 的盲区。远端 `found_bug id2460003`
已入库;当前 121 surfaces、98 roots、44 high、106 confirmed。上游
[#69827](https://github.com/pingcap/tidb/issues/69827) 已带 `found-by-ai`、`severity/major`、
`component/executor`。这与 #69796 不同:#69796 owns `LastInsertID/LastInsertIDSet`,本 root owns
`InsertID`;只应用 #69796 的清理不能修复。资产入口:
`assets/store/pessimistic-retry-insert-id-results.jsonl`、
`docs/method-cases/ai-native-id2460003-protocol-output-retry-method-case.md`、
`scaffolds/tidb-tests/ai_native_pessimistic_retry_insert_id_test.go`。该 owner 已 terminal;禁止枚举
显式 ID、INSERT 形状、delay 或冲突变体。下一轮从不同 terminal output owner 继续。

---

**2026-07-14 current-source pass 再命中 high correctness root `id2430003`:悲观 RC retry 会复用失败
attempt 已完成的 materialized CTE,成功提交 mixed-attempt row。** 候选从 #69823 的负边界产生:Go AST
scanner 把 `pkg/expression` 600+ Clone 压缩后,非 RAND 候选为零,因此把 owner 边界外移到 statement-owned
materialization。`StmtCtx.CTEStorageMap` 为每个 CTE 持有 storage、producer 和 `sync.Once`;第一次
attempt 完成 `resTbl` 后,`CTEExec.Close` 故意保留 completed result。retry build 继续使用同一 statement
context,`initOnce` 阻止 producer 重建,而 ordinary source read 已切到新的 RC attempt。

最强自然矩阵让同一 UPDATE 同时读取 ordinary `src` 和被引用两次、`EXPLAIN` 含 `CTEFullScan` 的 CTE。
竞争 session 在 CTE 的 SLEEP 窗口把 source 从 `next_u=1,payload=10` 改为 `2,20`,并占用 unique
`u=1`。local baseline `Exec_retry_count=1`,成功提交 `(u=2,v=10)`;同 final DB state direct control
提交 `(2,20)`。只在 retry build 前 `resetCTEStorageMap + empty map` 的 owner 反事实全绿。8-region、
22,051-byte bounded source packet 也只返回该 high-confidence candidate,并退役 partial CTE、correlated
parameter 和 cross-statement sibling。

testbed `8220955` 的三真实 TiKV SQL-only lift 同样 RED:retry rows=`1:2:10,2:1:0`,control=
`1:2:20,2:1:0`;slow log `Exec_retry_count=1,Exec_retry_time=20.00218629,Query_time=20.005502414,
Succ=1,IsExplicitTxn=1`,MDL 全程 default ON,无 failpoint,数据库已删除。新 selector 是
`REPLAY_PERSISTENT_MATERIALIZATION_STATE`,寻找
`initialize once -> preserve completed -> reset only at outer boundary` lifecycle 与 inner replay 的错位。
新强 oracle `MIXED_ATTEMPT_ROW_COHERENCE` 把 fresh path 和 materialized path 汇入同一行;结果若不属于
old/new 任一 coherent generation,直接判 RED。远端 `found_bug id2430003` 已入库;当前 120 surfaces、
97 roots、43 high、105 confirmed。上游
[#69826](https://github.com/pingcap/tidb/issues/69826) 已带 `found-by-ai`、`severity/major` 和
`component/executor`。资产入口:`assets/store/pessimistic-retry-stale-cte-results.jsonl`、
`docs/method-cases/ai-native-id2430003-cte-materialization-retry-method-case.md`、
`scaffolds/tidb-tests/ai_native_pessimistic_retry_stale_cte_test.go`、`tools/clone-state-scan/`。
该 CTE lifecycle 已 terminal;禁止枚举 recursive/nonrecursive、consumer count、SQL verb、delay 或 conflict
变体。下一轮只复用到不同 materialization owner 或 replay boundary。

---

**2026-07-14 current-source pass 命中新的 high correctness root `id2400003`:悲观 RC 透明重试会
推进 constant-seed `RAND` 的内部 RNG,把 duplicate-key failure 变成成功提交。** 候选没有来自 PR
review 或 issue seed,而是把 #69822 的 cross-attempt feedback selector 从 external owner 扩展到
prepared evaluator owner。`builtinRandSig` 在 statement prepare 前创建 mutable `MysqlRng`,
`evalReal.Gen()` 推进状态,`Clone` 浅拷贝同一 pointer;失败 attempt 消费第一个 deterministic value 后,
retry 从已经推进的 expression state 重建并消费第二个值。

最强矩阵把 `RAND(12345)` 的前两个值映射到相反终态:`IF(RAND(12345)<0.8,1,2)`。session B 在 A
第一次 evaluation 时提交 unique `u=1`;A hidden retry=1 后改取 `u=2`,返回成功并提交
`(1,2),(2,1)`。同 final DB state 的 direct execution 从第一个值开始,返回 duplicate key 并保留
`(1,10),(2,1)`。numeric sibling 为 retry=`912825259`、direct=`665703432`。local baseline RED、
owner-level retry-decline GREEN、以及 testbed `8220955` real-TiKV SQL-only RED 均完成;MDL 全程为
default ON。slow log 记录 `Exec_retry_count=1,Succ=1,Query_time=40.004s`。

新 selector 是 `MUTABLE_EVALUATOR_STATE_SURVIVES_RETRY`:枚举 `Clone` 中 pointer/map/slice/interface
alias,证明 first attempt 的 mutating method、retry rebuild 的 reuse,以及 key/predicate/row/terminal C3
consumer。新增 temporal gate:在 retry construction 时 deep-copy 已经太晚;该反事实仍 RED,说明 snapshot
altitude 必须早于第一次 mutation,或直接拒绝透明 replay。远端 `found_bug id2400003` 已入库;当前
119 surfaces、96 roots、42 high、104 confirmed。上游
[#69823](https://github.com/pingcap/tidb/issues/69823) 已带 `found-by-ai`、`severity/major` 和
`component/expression`。资产入口:
`assets/store/pessimistic-retry-seeded-rand-results.jsonl`、
`docs/method-cases/ai-native-id2400003-seeded-rand-retry-method-case.md`、
`docs/bug-drafts/ai-native-pessimistic-retry-seeded-rand-wrong-result-draft.md`、
`scaffolds/tidb-tests/ai_native_pessimistic_retry_seeded_rand_test.go`。该 RNG owner 已 terminal;禁止枚举
seed、threshold、random-function、DML、sleep 或 conflict 变体。下一轮只复用 selector 到不同 mutable
evaluator owner 或不同 correctness path。

**同轮补齐 `id2370003` 的方法资产:** #69822 证明 survivor 本身不是最强条件,关键是
`failed-attempt write -> retry read -> durable output`。对外部 owner 使用 equal-final-owner-state
anti-oracle:SETVAL retry/control 都留下 `NEXTVAL=101`,但 durable rows 是 NULL/100。它现在以
`HIDDEN_ATTEMPT_FEEDBACK_INTO_RETRY_OUTPUT` 写入 selector、proof catalog 和 methodology;sequence
变体仍为 terminal。

---

**2026-07-14 MDL-on current-source pass 命中新的 high root `id2310003`:悲观 RC 透明重试会保留
失败 attempt 获取的 advisory lock。** 本轮先把 testbed `8220955` 的
`tidb_enable_metadata_lock` 从旧实验残留的 `0` 恢复为 `DEFAULT=1`,全程没有并发 DDL。候选来自
S45 retry rollback owner 图,未使用 PR review/issue seed:`GET_LOCK` 修改
`session.advisoryLocks` 并维持独立 internal pessimistic txn,而 `StmtRollback + ResetForRetry` 只恢复
statement KV/executor/context。

最小自然矩阵使用 row-dependent lock name 防止 constant evaluation。A 的 pessimistic RC UPDATE 先
执行 `GET_LOCK+SLEEP`,B 在窗口内占用 unique key 并插入 gate;A 内部 retry=1,成功 attempt 因 gate
变成 zero rows。local RED 后才进入 testbed。real-TiKV slow log 记录 `exec_retry_count=1,succ=1`,
ROW_COUNT=0,但 `IS_USED_LOCK` 等于 A connection ID,竞争 session `GET_LOCK=0`;A cleanup release 后
竞争变成 1。同 final DB state、无失败 attempt 的 control 为 `IS_USED_LOCK=NULL,competitor=1`。

新方法增量是 **external capability consumer**:retry closure 不只审计 re-entry value 和 terminal
publication,还要审计 lock/lease/registration/handle 等独立 owner;强 oracle 是 owner identity + competing
denial + cleanup recovery + same-final-state control。远端 `found_bug id2310003` 已入库,当前 116
surfaces、93 roots、39 high;上游 [#69820](https://github.com/pingcap/tidb/issues/69820) 已带
`found-by-ai` 与 `severity/major`。资产入口:
`assets/store/pessimistic-retry-advisory-lock-results.jsonl`、
`docs/method-cases/ai-native-id2310003-advisory-lock-retry-method-case.md`、
`docs/bug-drafts/ai-native-pessimistic-retry-advisory-lock-leak-draft.md`、
`scaffolds/tidb-tests/manual/ai_native_pessimistic_retry_advisory_lock_test.go`。暂停 advisory lock 的 SQL、lock
name、retry count、sleep/reference-count 变体;下一轮只能复用到不同 external capability owner。

---

**2026-07-14 current-source-only 命中新的 severe root `id2280003`: MDL 关闭时,1PC 的 schema
校验只覆盖 prewrite 前的旧时刻,可在相关 DDL 完成后仍按旧 schema 原子提交。** 生成候选前没有使用
PR review、issue 或历史修复;先从 fast-path validation point `V` 与 atomic apply point `H` 的证明差
产生 P/Q/F。TiDB 在 `needCheckSchemaByDelta` 时安装 SchemaChecker;client-go 在
`calculateMaxCommitTS` 里于 `beforePrewrite` 前检查,TiKV 1PC prewrite 直接生成 committed write,成功
分支不会执行 2PC 的 actual-commitTS schema check。

local TRUNCATE 矩阵先 RED:1PC 返回 nil、当前表空、旧 table ID 下有 committed value;2PC control
安全重试。该 RED 没有直接升级,因为 INSERT 与 DDL 重叠,墙钟返回顺序不是 serialization oracle。live
脚手架随后同时抓 DML `commit_ts` 与 DDL history `FinishedTS`;testbed `8220955` 真实 TiKV 证明
`commit_ts > FinishedTS`。`ADD INDEX` 的 strongest cell 在 async commit 仍开启时实际 mode=`1pc`,
table scan=`1:10`,FORCE INDEX=空,`ADMIN CHECK TABLE` 失败;同点 2PC 为 `1:10/1:10 + ADMIN green`。
TRUNCATE sibling 是 INSERT success 但 replacement table 空。只在 `needCheckSchemaByDelta=true` 时
设置 `Enable1PC=false` 的单变量反事实令 local 全绿。

新 selector `VALIDATION_HORIZON_COVERS_IRREVERSIBLE_APPLY`:对 `V -> 可变世界 -> H` 计算
“H 消费且 `(V,H]` 可变化、又无 lock/version/CAS/revalidation owner”的事实;最小矩阵是 fast/safe
path x change altitude x highest consumer。新增硬门:重叠操作必须先用 commitTS/FinishedTS、epoch 或
version 证明逻辑顺序,不能拿 response order 直接宣称数据丢失。post-RED 去重无 exact root;#24009
只是旧 unstable skipped test 且称无 production impact;`id1440001` 是 async false abort,本条是 1PC
false success + 持久索引/表 identity 错误。资产入口:
`assets/store/txn-1pc-schema-horizon-results.jsonl`、
`docs/method-cases/ai-native-id2280003-onepc-schema-horizon-method-case.md`、
`docs/bug-drafts/ai-native-onepc-schema-check-horizon-corruption-draft.md`、
`scaffolds/top-level/ai_native_onepc_schema_horizon_probe.sh`。暂停本 root 的 DDL/delay/row-count 变体;
远端 `found_bug id2280003` 已入库为 confirmed/high,当前为 115 surfaces、92 distinct roots、38 high、
100 confirmed。下一轮继续 common transaction,排除 SAVEPOINT、partition 和已退役的 1PC response
ambiguity。

---

**2026-07-14 1PC response-ambiguity candidate 已由真实 TiKV 证伪并退役,不计新 bug。** current-source
候选是首个 TryOnePc 已提交但 response 丢失,随后 Region split 令 retry 遇到 EpochNotMatch 并清除
client-go 当前 1PC mode。本地 embedded mock 返回普通 write conflict,同时两 key 已可见且
`undeterminedErr=nil`;保留 request-scoped fast-commit RPC ambiguity 的反事实会返回 explicit
undetermined。但这只是 provisional RED:真实 TiKV 的 `check_committed_record_on_err` 会识别本事务已有
commit record,返回原 `one_pc_commit_ts`。testbed `8220955` 用独立进程执行真实 Region split 后,Commit
返回 nil、`commitTS > startTS`、两 key 都可见并已清理。因此该项入资产库为
`INVALID(semantic-gap)+GREEN(real-owner)`,没有写入远端 bug 库。

方法论新增硬门:跨层 retry 的 local RED 必须继续穿过真实 downstream idempotency/committed-record owner,
不能把 mock 的 retry response 当产品语义。测试编排也要隔离 owner:第一次同进程 split 被 process-wide
failpoint 一并暂停而死锁,有效矩阵改为“主进程 pause + 独立进程 topology actor”。下一步仍排除 partition、
SAVEPOINT 和这个 1PC 形状,对 common transaction 的 pipelined/fast-commit proof horizon 编译不超过三个
候选的 bounded packet。

---

**2026-07-14 事务跨层 campaign 已命中首个 severe root: `id2250003`。** 候选来自 current-source
`COLD_SOURCE` 审计,没有用 issue、PR review finding 或历史修复选题。client-go 在 async prewrite 全部成功、
`minCommitTS` 非零之后才执行 24 小时事务年龄检查,因此可以返回普通 `txn takes too much time`;如果随后
best-effort cleanup 不可用,TiKV 的独立 LockResolver 已经拥有完整 secondary 集合,会推导非零 commitTS
并把事务恢复为 committed。用户看到的是 COMMIT 明确失败,稍后写入却可见;应用按失败重试时可能重复执行
业务动作。local RED、仅移动 guard 的反事实 GREEN、以及 testbed `8220955` 上一 PD 三真实 TiKV 的 RED
均已完成,真实 TiKV 日志记录两把锁 `ResolveLock action=commit`。探针已删除专用 raw keys,集群无残留
failpoint/二进制,TiDB/client-go/TiKV 三个 pinned worktree 均已确认 clean。
当前 client-go `01bd8f99` 再次稳定复现后,已提交上游
[TiDB #69831](https://github.com/pingcap/tidb/issues/69831),带 `found-by-ai`、
`severity/critical`、`component/tikv-client`、`sig/transaction` 和 `type/bug` 标签;远端
`found_bug id2250003` 已同步为 `issue-filed`。
issue 已补齐真实生产触发边界:`tidb_enable_async_commit=ON`(当前默认 OFF)、事务 startTS 超过
24 小时、async prewrite 全成功、TiDB A 到 TiKV 的网络故障或 Pod 终止让后台 rollback 拖过 lock TTL;
业务在 TiDB B 上重试同一组 key 时会亲自触发恢复器提交第一笔,随后第二笔再提交,形成双重执行。

新 selector 为 `POST_PROOF_FALLIBLE_EPILOGUE`:先标出独立 owner 已能完成终态的最早 proof horizon `H`,
再审计 `H` 之后的每条 fallible edge。普通 abort 必须满足三者之一:把 guard 移到 `H` 前、返回
success/explicit-undetermined、或在返回前同步证明补偿完成。最小矩阵是 `guard altitude x compensation
availability x independent recovery consumer`;只注入 guard predicate 与补偿可用性,下游恢复 owner 保持
真实。资产库现为 367 revisions、RED=72/GREEN=69/INVALID=12/INFO=1、validated targets=43;远端
`found_bug` 为 114 行,其中 high=37。该 root 后果高,但自然频率受限于“事务超过 24 小时 + cleanup 未完成”,
不得包装成高频问题,也不要枚举 TTL/key-count 变体。

下一轮仍排除 partition 和 SAVEPOINT。先从 common-transaction proof horizon 寻找自然可达性更高的
post-proof error/cancel/timeout edge,优先普通 COMMIT、pessimistic retry、1PC/async fallback 的终态分叉。
每个 owner graph 继续使用 bounded source packet;local RED 前不碰 testbed,RED 后才允许 GitHub 去重。
FK cascade + child concurrent DDL 的 implicit-resource 假设已用 runtime rematerialization + index rowset oracle
证伪并退役,不要重复。

---

**2026-07-13 `id1740003` 新 high-severity 命中:一次 runaway watch 批量写错误会永久丢掉整批规则,使跨 TiDB KILL/COOLDOWN 静默失效。** 候选来自 current-source error-path owner 审计,没有使用 PR/review finding:`markQuarantine` 先把规则装入检测节点本地 watchList,再交给异步 `batchFlusher`;`flushFn` 返回错误时只增加 error metric,随后仍无条件替换 buffer,且没有 retry/WAL/reconcile owner。本地 failed-then-healthy 测试 RED:第二次 flush 根本不再调用 publisher(`attempts=1`);仅把 reset 移到成功分支后 GREEN。testbed `8220955` 双前端 real-PD/TiKV lift 也 RED:先在 A 以 100ms 触发 `WATCH EXACT`,再把阈值改为 24h 排除重复检测;同一 SQL 在 A 返回 8254,共享 watch 表无行,B 正常完成。无故障控制持久化一行且 A/B 都返回 8254。新 selector `FAILED_PUBLICATION_RETAINS_RETRY_OWNERSHIP`:错误处理之后必须证明精确 payload 仍由某个 owner 持有,不能把 log/metric 当作 retry。远端入库 `id1740003/high/confirmed`;post-hit asset/GitHub issue 搜索无重复。资产见 `assets/store/runaway-watch-flush-loss-results.jsonl`。

**2026-07-13 BR GC protection source candidate 已经 real-TiKV 证伪并退役,不计新 bug。** 候选完全来自 current source:`CheckGCSafePoint` 吞掉 `GetGCSafePoint` 错误,global `SetServiceSafePoint` 在 PD 返回的实际 safepoint 已晚于 backupTS 时也只 warning 并返回 nil。测试把 GC 时钟压到 1 秒,真实提交 old row、捕获 backupTS、更新为 new row,再由真实 PD/TiKV 完成物理 GC。正常 BR 在前置检查退出 1;只注入两次 safepoint read failure 后,primary guard 和 service write 都继续,但 `BuildBackupRangeAndInitSchema` 的 TiKV snapshot owner 以 9006 拒绝,BR 仍退出 1 且无 `backupmeta`。新 selector `GC_PROTECTION_ACK_DOMINATES_HISTORICAL_READ` 保留,同时给 LOOP 增加 layered-dominance gate:可疑 guard 被绕过后,必须继续追踪所有下游独立 owner,只有无效状态穿过全部层并到达成功终态/产物才算 RED。

**2026-07-13 `id1710003` 新 high-severity 命中: 已取消的 `ALTER RESOURCE GROUP` 仍会把未提交配置留在 PD 运行时。** 候选从 current source 的双 owner 提交顺序产生,未使用 PR/review finding。`onAlterResourceGroup` 先在 DDL worker txn 中 stage metadata,再立即调用 PD `ModifyResourceGroup`,之后才发布 schema/job;该 job 仍可取消,而通用 rollback 没有 resource-group 补偿。local scheduler 和 real-PD 两层都 RED:在 PD 修改后暂停,另一 session `ADMIN CANCEL DDL JOBS` 返回 successful,原 ALTER 返回 8214,history=cancelled,`SHOW CREATE` 仍是 `1000/LOW`,但 PD-backed `INFORMATION_SCHEMA.RESOURCE_GROUPS` 已是 `1/HIGH`;worker 记录 job-row write conflict 后走 generic cancellation。禁用 pause 后正常 ALTER 到 `2000/HIGH`,两个 owner GREEN。新 selector `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE`:所有 local transaction + external side effect 路径都要画 durable-boundary ledger,逐一检查 external-success 后仍可到达的 cancel/conflict/owner-loss 边,并要求 compensation/reconcile。远端已入库 `id1710003/high/confirmed`;post-hit GitHub 去重无匹配。停止枚举 RU/priority/runaway 值变体。

**2026-07-13 `id1680003` 新 high-severity 命中: BR 在 PD scheduler removal 失败时可能退出 0,但根本没有产生备份。** 候选来自 current source 的错误身份证明义务,没有使用 PR/review finding 选题。`RunBackup`、raw/txn/EBS backup 和 resolve-KV-data 五个顶层路径都把 scheduler-removal 错误绑定到 `e`,检查 `e != nil`,却返回之前 setup 的旧 `err`;当 setup 成功时旧值为 nil。local real-TiKV 命令级 RED:注入 `RemoveSchedulers` sentinel 后 BR 退出码为 0,摘要同时写 `Txn Backup failed`,备份目录为空且没有 `backupmeta`;无故障同命令 GREEN,写出 285-byte `backupmeta`;只把返回值改为 `errors.Trace(e)` 的反事实在同一故障下退出 1。新 selector `CHECKED_ERROR_MUST_DOMINATE_TERMINAL_RESULT`:不能只检查“有没有 error check”,还要追踪被检查错误的身份是否支配最终 return/ack/commit,并把外部终态与不可逆动作/产物联合成 oracle。远端已入库 `id1680003/high/confirmed`;命中后 GitHub 去重无匹配。停止枚举五个 sibling,后续只做 fix validation 或不同 terminal owner 的新 root。

**2026-07-13 `id1650002` 新 high-severity 命中: BR abort 持有 registry 行锁时会压住自己用来判断存活的 heartbeat,从而删除仍在运行的 restore。** 该候选完全从 current source 的 `P/Q/F` 产生,没有用 PR/review finding 选题:`FindAndDeleteMatchingTask` 在一个 pessimistic transaction 中先 `FOR UPDATE` 锁住 matching row,随后等待另一 session 更新同一行的 `last_heartbeat_time`;锁因此让真实 heartbeat `UPDATE` 命中 `kv:9007 Write conflict`,五次不变后代码把 live task 判 stale 并按 ID 删除。local real-TiKV 压缩时钟矩阵稳定复现 3/3:同一 writer 在锁外先被证明 heartbeat 可推进,锁内冲突后 abort 返回非零 ID 且 registry row=0;无 heartbeat 的真正 stale control 3/3 正常删除。上层 `restore.go` 对非零 ID 继续 cleanup checkpoints 并报告成功,但本 harness 只实测到 registry deletion,因此严重性定为 high,不宣称已实测数据损坏。新 selector `OBSERVATION_LOCK_SUPPRESSES_LIVENESS_SIGNAL`:任何 heartbeat/lease/progress stale 判定都要建立 observer-held resource 到 signal-writer lock/write set 的干扰图,并做 before-lock GREEN / after-lock RED 高度差分。命中后才做 upstream issue 去重,三组关键词均无匹配。远端 `found_bug id1650002/high/confirmed` 已入库,当前 94 行/71 roots。资产:`docs/bug-drafts/ai-native-br-abort-lock-suppresses-live-heartbeat-draft.md`,`docs/method-cases/ai-native-br-observation-lock-liveness-method-case.md`,`assets/store/br-abort-live-heartbeat-results.jsonl`;暂停门:不枚举 heartbeat interval、status 或 restore filter 变体。

**2026-07-13 PR #66217 review finding 已被测试 LOOP 转成稳定 held-out RED,但不计新 bug。** 从 current guard `checkIndexJoinInnerTaskWithAgg` 的证明义务出发,LOOP 独立推到 `GROUP BY a+b` 反例:guard 只检查 inner key `a` 是否出现在表达式列集合,却没有证明 `a` 支配聚合 group。两行 `(1,1025,10)` 与 `(1025,1,20)` 的 group 都是 `1026`;两-key 单 task 控制下 IndexJoin/HashJoin 都返回 `(1,3,30)`,会掩盖问题。把 outer clustered keys 扩到 `1..1025` 并设 `tidb_index_join_batch_size=1` 后,`EXPLAIN ANALYZE` 证明 `IndexJoin task:33`,IndexJoin 返回两条局部结果 `(1,1026,10),(1025,1026,20)`,而 HashJoin 返回全局结果 `(1,1026,30)`。精确去重命中 merged PR #66217 的 AI review P1,所以 target 以 held-out/retired 入库,不新增 bug/root。新方法资产 `PARAMETER_KEY_DOMINATES_STATEFUL_GROUPING`:状态算子进入参数化执行时,参数 key 必须精确包含 state key、通过 functional dependency 支配它,或保证所有 task 共享一个全局 state domain;最小矩阵是 one-task GREEN / cross-task RED / global-reference。资产:`assets/store/indexjoin-agg-expression-heldout-results.jsonl`;方法 case:`docs/method-cases/ai-native-indexjoin-agg-expression-heldout-method-case.md`。

**2026-07-13 `id1500003` 新命中: `FLASHBACK DATABASE` 恢复 sequence 时会丢失 sequence runtime value,导致 `NEXTVAL` 回卷并复用已有 ID。** 在 testbed `8220955` current master 上,创建 `seq` 和 `t(id DEFAULT NEXT VALUE FOR seq PRIMARY KEY)`,先插入两行得到 `id=1,2`,再 `NEXTVAL(seq)=3`;执行 `DROP DATABASE` 后 `FLASHBACK DATABASE`,恢复后的表仍有 `1,2`,但 `NEXTVAL(seq)` 返回 `1`,下一次默认插入报 `ERROR 1062 Duplicate entry '2' for key 't.PRIMARY'`。控制格显示:无 recovery 的同形状 sequence 正常走到 `1,2,3,4`;普通 `AUTO_INCREMENT` 表经 `FLASHBACK DATABASE` 后不会复用 `1,2`。源码链路是 `onRecoverSchema` 从 snapshot `ListTables` 拉出 sequence 的 `TableInfo`,统一调用 `recoverTable -> CreateTableAndSetAutoID`;但 sequence create/drop/runtime 使用独立 `sequenceKey`,不在 `AutoIDGroup` 内。新 selector 为 `RESTORE_SPECIAL_OBJECT_STATE_REBUILD`:恢复路径不能只证明 `TableInfo` 被重建,还要检查特殊对象的 runtime side state 是否一起重建。证据:`assets/store/logs/flashback-db-sequence-reset-red-20260713.log` 和 `...-controls-20260713.log`;资产:`assets/store/flashback-db-sequence-reset-results.jsonl`;draft/case 已入 `docs/bug-drafts/ai-native-flashback-db-sequence-reset-draft.md` 与 `docs/method-cases/ai-native-flashback-db-sequence-reset-method-case.md`。当前为 issue-filed / C3_DIRECT,远端 `found_bug` id1500003 已入库,上游 issue 为 [#69781](https://github.com/pingcap/tidb/issues/69781),带 `found-by-ai` 与 `severity/major`。

**2026-07-12 `issue59701` topology lift 收口为当前环境 capability boundary。** 在 testbed `8220955` 的
`fp-tidb` 上，先用 `resign-owner` 在 active `write reorganization` 窗口执行一次，再用 300000 行、64
regions 的长窗口连续执行 4 次 owner resign；job 5254 始终可恢复，最终 `synced/public`，
`ADMIN CHECK TABLE`、table/index count 和 `idx_c` 行集均一致。这个结果是强 GREEN，但只覆盖同一 TiDB
实例的 owner resignation/re-election，不覆盖 PD leader isolation 或有独立 survivor 的跨实例 handoff。
因此 `target.seed.issue59701...` 已标为 `blocked`，不是 bug，也不再重复同形状 GREEN。资产为
`assets/store/issue59701-resign-owner-results.jsonl` 及两份 live log。

**本轮严重性调度正式停机。** `issue61255` 的当前非 partition 形状因 merge `workerCnt=1` 被记录为
`INVALID(target-shape)` 并退役；`issue59701` 的同实例 topology 控制已有强 GREEN；新的
`state-ingress`、pooled-session、session-state-restore 源码扫描没有产生新的候选，terminal-action
扫描只产生 consequence-1。资产库当前没有可执行的 `C3_DIRECT` target，`store.py next` 返回
`no severity-admitted targets`。下一轮应先补充新的高后果 selector 或可达的 multi-owner/PD fault
能力，再继续执行；不要为了保持 bug 数量而扩展低严重性矩阵。

**方法论新增 GREEN-only exhaustion gate。** 当一个 target 已有强 GREEN，但 GREEN 发生在目标证明义务
所需的控制维度不存在时，结果必须拆成“观察结果”和“目标有效性”：前者可作为 negative boundary，后者
进入 `blocked`/`INVALID(target-shape)`，不能成为 family-wide safety proof。只有同时具备 active phase、
真实 fault ingress、独立 survivor/owner handoff 和 consequence oracle，才允许再次消费该 C3 target。

**2026-07-12 `id30001` 从候选提升为 current-master confirmed / issue-filed。** 在 testbed `8220955` 重跑一个不含 NULL 的五行表：`INDEX pi(b) WHERE a < 3`，查询 `WHERE a >= 0 ORDER BY b LIMIT 5`。`IGNORE INDEX(pi)` 返回五行，而默认计划和 `FORCE INDEX(pi)` 都只返回 `a<3` 的三行；`EXPLAIN` 明确走 `IndexLookUp(Build=pi)`，`ADMIN CHECK TABLE` 无输出。上游 issue 为 [#69779](https://github.com/pingcap/tidb/issues/69779)，远端 `found_bug.id30001` 已更新为 `status=issue-filed,confirmed=1`。当前根因表述收窄为：`CheckPartialIndexes` 对 raw metadata predicate 的输入路径与 query predicate 的 normalizer 不同，range implication checker 可能把 under-normalized/full range 当成证明；这使“proof input normalization”成为证明义务的一部分。新方法案例为 `docs/method-cases/ai-native-id30001-method-case.md`，证据为 `assets/store/logs/partial-index-implication-red-20260712.log`。这不是新的测试添加，而是把“源码证明义务 -> 语义反例 -> fast/safe path 差分 -> 强 wrong-result oracle”固化成可复用 selector S29；hint、no-hint 属于同一 root 的 blast radius，不重复计数。

**2026-07-12 `issue61255` non-partition 宿主筛选给出一条无效绿边界。** 复用 mixed-owner probe，在 `requested_workers=4`、`ADD UNIQUE INDEX idx_b(b), ADD INDEX idx_c(c)`、merge pause 和 `ADMIN CHECK TABLE` 强 oracle 下，job `5176` 最终 `synced/public` 且 rowset 全绿；但 owner log 明确显示 `type="merge temporary index" workerCnt=1 regionCnt=2`。因此这个 run 不能证明 mixed-owner merge 安全，也不能算新 bug；它证明了 C3 harness 必须在目标 phase 检查 controlling dimension 是否真的存在。后续只有在 merge `workerCnt>1` 或出现另一个 live owner-homogeneity 维度时才重开该 lane，证据已入 `assets/store/issue61255-mixed-owner-results.jsonl`。

**2026-07-12zp `id1530002` 完成上游去重,不再算新的 bug root。** current-master 的 live probe 仍证明:删除正在被打开 Pebble engine 消费的内部 `000004.sst` 会让执行 subtask 的 TiDB front 返回 `ERROR 2013` 并退出,随后 DXF 才由其他 front 接管;相邻 raw input SST 删除则是 retry/rebuild GREEN。上游 [#65958](https://github.com/pingcap/tidb/issues/65958) 已报告同一 failure boundary:`ADMIN CANCEL DDL JOBS` 后 job 行消失,`tmp_ddl` cleanup 删除仍在使用的目录,最终 Pebble missing-SST fatal 退出 TiDB;[#66187](https://github.com/pingcap/tidb/pull/66187) 是对应修复且目前仍 OPEN。因此 `id1530002` 已改为 `known-duplicate/high`,保留为 current-master 重放、asset-type GREEN control、进程/owner/task oracle 和 fix-validation 资产,不重复提 issue、不新增 severe root。方法论新增 strong RED 后的 upstream history/issue dedup gate:用 operation+lifecycle action+phase+asset/consumer edge+fatal signature 组成精确 tuple,再按 issue/PR/history 搜索;相同 trigger、failure boundary、fix locus 就归为 rediscovery。method case:`docs/method-cases/ai-native-id1530002-method-case.md`。**

**2026-07-12zl `id1530002` 把 distributed `ADD INDEX` 的 runtime-asset-loss candidate 又收紧成了可复用的红绿对照。** 在 testbed `8220955` 的 current dirty build 上,`job 5151 / task 630002` 先在 `SetTSBeforeImportEngine` 后停住,删除 **Pebble local engine DB 内部**的 `<engine_uuid>/000004.sst` 所在目录再放行;4000 前台返回 `ERROR 2013 Lost connection`,4000 TiDB 进程从 `/proc` 消失,日志打印 `orig err/list err`,随后同一 task 从 `exec_id=4000` 搬到 `4001`,job 在 `11:18:23 UTC` `synced/public`,table scan/forced index/default scan 均为 `10000`,`ADMIN CHECK TABLE` 通过。紧邻的 `job 5148` 对照在 `pauseBeforeLocalDBIngest` 时删除 raw input SST,只得到 `context canceled -> retry -> folder is empty -> rebuild -> synced`,没有进程退出。这个结果纠正了之前过宽的表述:不能把“输入 SST 缺失”和“已打开 Pebble DB 引用的内部 SST 缺失”视为同一资产。远端 bug 库新增 `id1530002` (`candidate/high`,`dist-addindex-local-engine-db-loss-process-exit`),暂不按 confirmed root 计数,因为还需要 product contract 判断临时 DDL engine 损坏是否允许 fail-fast。证据:`assets/store/logs/add-index-local-engine-db-loss-red-20260712.log`;draft:`docs/bug-drafts/ai-native-dist-addindex-local-engine-loss-crash-draft.md`。方法论新增 asset-type control:每次 runtime fault 必须先标注 exact consumer edge,并用相邻资产类型做 GREEN control;最终表全绿不抹掉 availability RED,但要与 wrong-result/permanent-hang 分开记账。**

**2026-07-12zm `id1470001` 又补了一个更贴近线上事故来源的例子,并同步到上游 issue #69776。** 这次不再只描述“历史数据里有重复业务键”,而是把来源写成常见的 ambiguous commit:第一次订单写入已经在服务端提交,客户端在收到响应前超时,应用用新连接重试,表上还没有唯一约束,于是同一个 `order_no` 落到两个不同自增 `id`。随后在线执行 `ADD UNIQUE INDEX uk_order_no(order_no)`,等 job 进入 `write reorganization` 且较晚主键范围尚未回填时,前台延迟升高触发 DBA/资源控制自动化执行 `ADMIN ALTER DDL JOBS <job_id> THREAD=1`;晚范围 worker 的普通 `batchCheckUniqueKey` 会产生真实 `ERROR 1062`,这才是被取消 tail worker 可能丢失的 terminal result。复现说明现在给出可复制的两次 INSERT、`IGNORE INDEX`/`FORCE INDEX` 对照和 `ADMIN CHECK TABLE`,同时明确无 failpoint 仍是概率窗口:先检测到重复并 rollback 是 control,只有 `synced/public` 后路径分裂或 `8223` 才算 bug。**

**2026-07-12zn `id1500002` 的 identity-drift 候选又补出一个更强的 consequence sibling,仍不是新 root。** 在 `FLASHBACK TABLE` 恢复历史子表后,旧父表已被删除,新建的同名父表含同一个 key;带 `ON UPDATE CASCADE` 的历史 FK 仍被发布。testbed `8220955` 上,恢复后子行先是 `(10,1)`,执行普通 `UPDATE p SET id=2 WHERE id=1` 后被静默改写为 `(10,2)`,而 `ADMIN CHECK TABLE c` 仍通过。此前的 `ON DELETE CASCADE` 已证明同一 rebound reference 可以删除历史子行,这次证明也可以改写历史子行。方法论新增一条 recovery oracle 规则:结构检查和 future-write FK check 不够,恢复成功后必须对当前引用对象执行一次正常的 destructive/mutating action,再核对 recovered rowset;否则会漏掉“引用已经绑定到新对象但表面结构全绿”的高后果问题。证据为 `assets/store/logs/flashback-fk-same-name-update-cascade-red-20260712.log`,JSONL 已追加 run,root 仍为 `flashback-fk-rebinds-recreated-parent`。**

**2026-07-12zo 本轮 source-target 反推没有产生新的高严重 DDL 目标,按暂停门停止扩张。** `terminal-action-error` 规则在当前源码上只产生一个 consequence-1 的 Parquet writer 候选;DDL 侧命中的是已有 cleanup/defer 或已覆盖的路径,没有新的 C3 owner。当前队列里的 `issue61255` 仍是 partition 邻近且已有 live GREEN,`issue59701` 已有 topology GREEN control,`issue62531` 是已知 row-image family,所以不为“继续挖”硬造中低级 bug。LOOP 文档新增负缓存规则:当 source scan 没有新 high-consequence owner 时,保留筛选理由,回到 selector 设计或等待源码变化,而不是枚举同一 family 的更多动作。**

**2026-07-12zk `issue62531` 这轮把源码桥和 live 结果再次对齐,但没有新增 root。** 窄 instrumentation 明确记录 DELETE 的 row shape 是 `[id,val0,_col$_val0_0]`,而 table-scan 缺列时只能按 `DefaultVal` 填 changing slot;counterfactual 把它改成依赖列 cast 值后 shape 正确。当前 failpoint-enabled Linux/amd64 binary 在 testbed `8220955` 上重放非 partition `MODIFY COLUMN + secondary index + 16 worker insert/delete` 仍命中真实 `table_scan_executor.rs:467 Data is corrupted, missing data for NOT NULL column`;job `5049` 最终 `synced/public`,但 aftermath 的 `ADMIN CHECK`、table/index count(`2082/2082`) 和公式 oracle 全绿。新增 `val0 DEFAULT 7` 及单值域 sibling 也没有留下 stale index(`3000/3000`, `val0=7` 两路径均为 0),所以只记为同一 known issue62531 family 的负边界。方法论新增“cross-layer information-preservation gate”:先抓 producer row shape,再列 protocol fields,用 counterfactual 验证依赖元信息是否丢失,最后区分窗口错误与持久坏后态。证据与资产已入 `docs/method-cases/ai-native-issue62531-row-image-bridge.md`、`assets/store/issue62531-row-image-bridge-results.jsonl`。**

**2026-07-12zj `id1470001` 的真实复现条件又具体化了一层,并同步写回上游 issue #69776 与远端 bug 库。** 不再只写“in-flight 操作返回必须处理的错误”:线上可对应为 `orders` 表被旧 importer 超时重试写出两个相同 `order_no`、发布前在线补 `ADD UNIQUE INDEX uk_order_no(order_no)`,白天前台延迟升高时 DBA/资源控制自动化执行受支持的 `ADMIN ALTER DDL JOBS <job_id> THREAD=1`,而重复值位于较晚主键范围,使 busy tail worker 在 `batchCheckUniqueKey` 返回普通 `Duplicate entry '<order_no>'/ERROR 1062` 时已被缩容 cancel。命中后的用户结果明确是 `synced/public` 但 `IGNORE INDEX` 与 `FORCE INDEX` 行集不一致,普通查询可能漏订单,`ADMIN CHECK TABLE` 报 `8223`;如果 downscale 前正常 rollback,则是 control。当前仍诚实区分:无 failpoint 例子是高概率 production shape,不是 deterministic reproducer;确定性时序仍由 test-only failpoint 锁定。远端 `found_bug.id1470001.repro/notes` 已写入这套具体故事。**

**2026-07-12zg `FLASHBACK TABLE` identity-drift candidate 已完成 root accounting 并写入 bug 库为 `id1500002` (`candidate/high`, `root_cause_id=flashback-fk-rebinds-recreated-parent`)。** 在 testbed `8220955` 上,历史合法 `p/c` FK 经过 `DROP c; DROP p; CREATE TABLE p` 后执行 `FLASHBACK TABLE c`,恢复的 `c(1,1)` 对当前空 `p` 变成 `orphan_rows=1`;未来 INSERT 仍有 `Foreign_Key_Check`,但 `ADMIN CHECK TABLE c` 静默通过。它与 id30016 共享 S6 selector,但不是同一表面: id30016 的 missing-parent existence fix 会对当前 parent 存在的 empty-parent cell 放行,而本候选需要 row-membership 或 referenced-object identity proof。控制矩阵保持清晰:同名父带原行 GREEN,同名空/不兼容父 RED,`FLASHBACK DATABASE` GREEN。远端当前为 `MAX(id)=1500002, COUNT(*)=89, COUNT(DISTINCT root_cause_id)=66`;当前不发 upstream issue,先保留 candidate 并等待 product/fix review。证据:`assets/store/logs/flashback-fk-same-name-parent-rebind-red-current-20260712.log`。**
**2026-07-12zi issue62531 的历史形状校准完成,当前不再把单次 pinned GREEN 当作修复证据。** 在 testbed `8220955` 上,三条单 DDL sibling(`USE INDEX`,`IGNORE INDEX`,无二级索引)都触发了真实 apply-window 且强 oracle GREEN;更关键的是,把历史程序恢复成连续 `MODIFY COLUMN int -> bigint -> int`、带参数 prepared insert/delete 的 2 分钟循环,无索引完成 33 轮 DDL,有索引完成 35 次成功 DDL,最终 `ADMIN CHECK TABLE`/公式/index-table oracle 均 GREEN。由于当前 endpoint 报告 `v8.4.0` placeholder,而 issue62531 的历史环境是 `v8.5.2`,这只能记为版本/形状边界,不能宣布修复。方法论新增 replay shape gate:历史是 repeated DDL + prepared DML 时,one-shot hold probe 只能证明 phase capability,不能替代原始循环。证据已复制到资产仓 `assets/store/logs/modify-column-issue62531-repeat-changing-column-no-index-20260712.log`、`...-index-20260712.log` 及对应 calibration JSONL。**
**2026-07-12zh `id1500002` 的 consequence 已从 orphan 提升为直接数据丢失证据,但仍是同一 root。** 在 `8220955` 上,历史 `c(pid=1)` 带 `ON DELETE CASCADE` 恢复到同名新 `p(id=1)` 后,`FLASHBACK TABLE c` 成功;随后普通 `DELETE FROM p WHERE id=1` 使 `c` 行数从 `1` 变为 `0`,而 `ADMIN CHECK TABLE c` 仍通过,`SHOW CREATE TABLE c` 仍显示旧 cascade FK。这个结果证明 identity drift 不只是静态孤儿:新父对象的正常 destructive action 可以删除历史子数据。证据:`assets/store/logs/flashback-fk-same-name-cascade-delete-red-20260712.log`;资产 JSONL 已追加 consequence-escalation run,不新增 root。**

**2026-07-12zf id1470001 的 issue 复现条件已补成真实业务触发故事,并同步更新到上游 issue #69776。** 之前的 “in-flight 操作返回必须处理的错误” 现在具体化为:历史导入/应用重试留下同一 `email` 两行,运维在 `txn` backfill 路径上补建 `ADD UNIQUE INDEX`,作业运行数分钟后因前台延迟执行 `ADMIN ALTER DDL JOBS <job_id> THREAD=1`;包含后段重复值的 busy tail worker 被 cancel,随后返回普通 `Duplicate entry 'a@example.com'` 错误,但 `sendResult` 可能因 `ctx.Done()` 丢掉该错误,导致本应 rollback 的 DDL 进入 `synced/public`。这里明确了真实前置条件:`tidb_ddl_enable_fast_reorg=OFF` 或 ingest 不可用,避免把可能走 ingest 的 unique-index 作业笼统算进来。当前 testbed `8220955` 已用无 failpoint control 验证普通重复键确实从真实 txn backfill 返回 `ERROR 1062`;这只是错误来源 control,不是 deterministic race red。随后又把真实触发写成可操作的 production-shaped recipe:百万级历史表、较晚主键范围的重复值、`write reorganization` 期间 `8 -> 1` 线程降级,以及完成后的 `IGNORE/FORCE INDEX + ADMIN CHECK TABLE` 强校验;同时明确四行 SQL 只做 error-source control,不能冒充 race reproducer。证据已入 `ai-native-fuzz-assets/assets/store/logs/add-index-real-duplicate-control-20260712.log`,issue 草稿和 bug draft 已同步。**

**2026-07-12zd 资产化补账:common-reorg `ADD INDEX` downscale silent-publish severe bug 已正式写入远端 `found_bug id1470001`,并同步进入资产仓 severe index;随后已在 TiDB 上游提 issue https://github.com/pingcap/tidb/issues/69776,带 `found-by-ai` / `severity/critical` / `component/ddl` / `type/bug` 标签。** 这条就是前面 `job 4452` 的已发布坏索引问题:tail worker 在 post-batch sleep 后产生真实 error,但 `ADMIN ALTER DDL JOBS ... THREAD=1` 触发 downscale 后,被 cancel 的 tail worker 结果可能在 `sendResult(ctx.Done vs resultCh)` 里被静默丢掉,collector 于是把 partial backfill 当成成功,DDL 走到 `synced/public`。用户层后果是 `COUNT(*)` / `IGNORE INDEX` / `FORCE INDEX` 分裂,普通查询会走坏索引漏行,`ADMIN CHECK TABLE` 报 `ERROR 8223 data inconsistency`。远端当前状态已复核为 `MAX(id)=1470001, COUNT(*)=88, COUNT(DISTINCT root_cause_id)=65`;root_cause_id 为 `addindex-downscale-drops-tail-worker-error`,severity/status 为 `high/issue-filed`。资产仓 `/Users/bba/pc/ai-native-fuzz-assets` 已补 `docs/bug-index/SEVERE_BUGS.md` 和同步脚本,后续每轮要把 bug、selector、oracle、probe 当成可增量复用资产维护,不要只靠 handoff 流水账。**

**2026-07-12ze 新的严重候选: `FLASHBACK TABLE` 会把历史子表重新绑定到“同名但已被重建”的父表,并直接发布旧的孤儿行。** 在 testbed `8220955` 的 `127.0.0.1:14101` 上,先创建 `p(id=1)` 与 `c(pid=1)` 的合法 FK,删除两表,只重建空的同名 `p`,再执行 `FLASHBACK TABLE c`;恢复成功后 `c` 仍含 `id=1,pid=1`,但 `LEFT JOIN p` 得到 `parent_id=NULL`,`orphan_rows=1`,而 `EXPLAIN` 显示未来 INSERT 仍有 `Foreign_Key_Check`. `ADMIN CHECK TABLE c` 不报告该已有孤儿。直接创建同样的空父/子 schema 后再插入 `pid=1` 会立即报 `ERROR 1452`,因此这是恢复路径绕过既有行参照完整性校验,不是普通 FK 语义。源码锚点是 `executor.go:1459-1472` 只做 schema/name 检查,`table.go:183-198,296-300` 直接 clone/publish 历史 `TableInfo`,而 `model.FKInfo` 只保存 `RefSchema/RefTable` 名称,不保存被引用对象身份。控制矩阵已完成:同名父表带回原行 GREEN(`orphan_rows=0`),同名空父表 RED(`orphan_rows=1`),同名不兼容 `VARCHAR` 父键 RED(直接 FK 建表 `ERROR 3780` 但 `FLASHBACK` 成功),`FLASHBACK DATABASE` 双对象 GREEN。当前仍先记为 `high candidate`,不把它立即计成与 id30016 独立的 root;下一步只需按 fix locus 决定是新 root 还是 `flashback FK reference` 的 identity-drift surface。证据草稿、红格和矩阵已入资产仓:`docs/bug-drafts/ai-native-fk-flashback-same-name-parent-rebind-draft.md`、`assets/store/logs/flashback-fk-same-name-parent-rebind-red-20260712.log`、`assets/store/logs/flashback-fk-identity-drift-matrix-20260712.log`;方法论文档新增“identity drift”恢复维度,明确要求用旧 oracle 做一维变异并先查 existing rows,不能只查 future DML。**

## 0. 一句话现状
**2026-07-12zc 这轮把 `ADD INDEX merge-temp-index` severe harness 的“有效绿/无效绿”边界又向前推了一层:当前 testbed `8220955` 上,普通非 partition 顶层 `ADD UNIQUE INDEX` 终于被证明既能制造真实 merge workload,也能稳定停在 merge 前窗口,但它**仍然不是**当前 `downscale silent-publish` severe lane 的合格宿主,因为 merge 阶段会自动塌缩成 `workerCnt=1`。** 先说 capability 正向进展:两阶段 live 宿主 `ai_native_unique_twostage_cap_20260712`(`job 4706`) 已经补齐 owner 侧铁证——不是之前 `job 4701` 那种“pause 期插入了行,但 merge `added count=0` 的空 merge”。这次 `beforeBackendIngest=pause` 期间先补 `1000` 行,再在 `beforeBackfillMerge=pause` 前放行 ingest,owner 日志明确出现 `start to merge temp index` 后 `backfill-worker 3, tp merge temporary index ... added count=1000`,最终 `handled rows=33000`,table/index/admin-check 全绿。也就是说,**“非 partition 顶层 single unique index + stage1 DML” 这条 lane 已经具备真实 merge 工作量**,比之前 `pause-time-only inserts` 的假负载更接近 severe harness。真正值钱的是后面的负边界:在此基础上再挂完整 severe 形状 `mockBackfillPostBatchErrForWorker=tail + mockBackfillPostBatchErrSleepMs=30000 + ADMIN ALTER DDL JOBS ... THREAD=1`,新的 decisive run `ai_native_unique_twostage_severe4_20260712`(`job 4721`) 虽然用户层终态仍是 `synced/public` 且强 oracle 全绿,但这次我们没有再把它草率记成“共享根仍不扩散”。owner 侧控制日志说明这是一条**无效绿**:merge 入口前确实看到了 `ready to merge`,也确实看到了 staged DML 被 merge worker 处理(`added count=1000`),但 `start backfill workers to reorg record` 明确打印 `type=\"merge temporary index\" workerCnt=1 regionCnt=1`;随后**没有**出现 `adjust ddl job config success current worker count=1`,也**没有**出现 `mock backfill post-batch error injected`/`backfill worker failed`。换句话说,`ADMIN ALTER DDL JOBS ... THREAD=1` 虽然 SQL 返回成功,可在这个 exact host 上 merge 阶段其实从一开始就只有一个 worker,tail-error 也没有命中,所以这次绿格不能拿来支持“common severe root 不再扩散”,它只能支持更细的宿主结论:**`nonzero merge added count` 仍然不够;要打 `downscale` severe,至少还要再证明三件事同时成立:1) merge `workerCnt>1`;2) live downscale 真触发 `adjust ddl job config success`;3) failpoint 真触发 `mock backfill post-batch error injected`。** 这轮还顺手暴露了一个很实用的控制细节:对这个 unique-index lane,`beforeBackfillMerge` 的可见信号并不总是我们最初假设的 `ready to merge` 之前/之后固定位置,所以控制器不能死盯单一 owner log 事件;更稳的 merge gate 是 **`run reorg job done` 已出现 + SQL 侧仍稳定停在 `write reorganization,row_count=baseRows` 平台**。另外 `job 4721` 在 merge 入口前还短暂打出了一次 `mysql.tidb_ddl_job(handle=4721)` optimistic write conflict 并自动重试成功;当前它更像调度噪声而不是 bug,但方法论上提醒我们:live severe harness 必须把“目标 race 是否真的命中”与“owner/scheduler 自身的无害重试噪声”分开记账。**

**2026-07-12zb 这轮把“严重 bug harness 应该挂在哪个 DDL 宿主上”这件事又往前推进了一大步:当前 testbed `8220955` 上,`local ingest + multi-index ADD INDEX` 与 `MODIFY COLUMN` 都已经被纳入同一套 live probe 骨架,但只有后者同时满足“真实 merge/worker 形状”与“控制面支持 downscale”这两个条件。** 先说最值钱的 capability 结果:新 probe `/Users/bba/pc/ai-native-probes/add_index_ingest_multischema_merge_live_probe.go` 在 `127.0.0.1:14101/18182` 上实锤了 `local ingest + 两个普通索引` 的 merge lane 是活的:`job 4652` 在 `beforeBackfillMerge=pause` 下稳定停在 `row_count=32768`,pause 期间补进 `1000` 行后终态 `GREEN rows=33768`;这说明相较于之前 single-index ingest,**multi-index local-ingest 是当前 cluster 上更真实、更接近“多 task merge” 的宿主**。但同一 probe 也立即给出一个硬边界:`job 4657` 在挂上 `tail + post-batch error + THREAD=1` 时,`ADMIN ALTER DDL JOBS` 直接报 `unsupported DDL operation: alter table multi-schema change`;也就是说,**它有我们想要的 merge 形状,却没有我们想要的控制面入口**,不适合作为当前 severe `downscale` harness 宿主。于是这轮把宿主切到 `MODIFY COLUMN`,并新增了第二个 reusable probe:`/Users/bba/pc/ai-native-probes/modify_column_downscale_error_live_probe.go`。这个 probe 先在 row-rewrite stage 证明了一个更强的负边界:`job 4667` 上,`tail + sleep 30s + row_count>=1 即 THREAD=1 downscale` 最终仍然 `synced/public`,而且 `FORMULA_BAD=0`,`ADMIN CHECK TABLE` 通过,index/table point oracle 也一致;说明共享 `txnBackfillExecutor + tail cancel + sendResult` 这些公共构件本身还不足以自然长出 severe。更关键的是,这个 probe 进一步把 `MODIFY COLUMN` 的 **merge temp index** sibling 也收进来了:`job 4672` 在 `beforeBackfillMerge=pause` 下稳定停住, pause 期补进 `1000` 行后最终 `synced/public` 且强 oracle 全绿;随后 decisive sibling `job 4677` 直接把原始 severe 形状完整迁过来:`merge pause -> pause 期间补 1000 行 -> mockBackfillPostBatchErrForWorker=tail -> mockBackfillPostBatchErrSleepMs=30000 -> release 后 THREAD=1 downscale`。结果仍然是 `synced/public`,而且 `GREEN final_state=synced rows=61000`,`ADMIN CHECK TABLE`/formula/index-path oracle 全绿。换句话说,**当前 severe root 还没有自然扩到 `MODIFY COLUMN` 的 row-rewrite 或 merge-temp-index sibling;它比“共享 backfill worker + downscale + tail error”更窄。** 方法论上这轮新增三条实用规则:1) **先选宿主,再上矩阵**:共享 executor 只是必要条件,还要同时看“exact cluster 上的 real shape 是否活着”和“控制面是否真的支持目标动作”;`multi-schema ADD INDEX` 就是 shape 对了但 control 不对的典型。2) **capability probe 应先行**:在 exact testbed 上,先证明 `pause` 真能命中、增量 DML 真能进入最终 merge,再挂 error/downscale;否则很容易把源码里的好点子误投到当前 build 不走的 lane 上。3) 这轮两个 reusable probe 已经把宿主选择也资产化了:以后继续打 severe sibling 时,优先从 `modify_column_downscale_error_live_probe.go` 往前长,而不是回到 shell 脚本或只盯单一 `ADD INDEX` lane。**

**2026-07-12za common-reorg severe lane 这轮把一个很容易“想当然扩散”的怀疑正式打成了高价值负边界: `ADD INDEX` 上已经实锤的 downscale silent-publish race,当前**没有**自然扩到 `MODIFY COLUMN`。** 这次不是只做普通 control,而是专门把 live 触发条件修正到真正对的那一层:前几轮 split-table `MODIFY COLUMN` control(`job 4589/4597/4602/4607`)已经证明 `worker3 + mockBackfillPostBatchErrSleepMs` 能稳定把 row-rewrite 停在 `RowCount=57344`，但也暴露出一个关键方法学细节: `mockBackfillPostBatchErrSleepMs` 的 sleep 点发生在 **visible injected log 之前**,所以正确的 live 控制信号不是盯 `mock backfill post-batch error injected`,而是盯 `ADMIN SHOW DDL JOBS` 里 `write reorganization + RowCount=57344` 的平台期。最终 decisive run 是 `ai_native_modcol_split_downscale4_20260712`:`65536` 行、`split table ... regions 8`、`mockBackfillPostBatchErrForWorker=3`、`mockBackfillPostBatchErrSleepMs=30000`。在 control 日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-split-downscale-candidate-live4-control-20260712.log` 里,我们在 `job 4612` 仍处于 `write reorganization` 且 `RowCount=57344` 时,成功执行了 `ADMIN ALTER DDL JOBS 4612 THREAD = 1`；owner 日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-split-downscale-candidate-live4-owner-20260712.log` 随后明确记录了 `adjust ddl job config success current worker count=1`，之后才出现 `mock backfill post-batch error injected workerID=3`、`backfill worker failed`、`run modify column job failed, convert job to rollback`，最终 `rollback done`。用户层结果也非常干净:`ALTER TABLE ... MODIFY COLUMN` 直接返回 `ERROR 1105 mock backfill post-batch error on worker 3`，最终 history 是 `rollback done`，表结构保持原 `int`，`ROW_COUNT=65536`,`FORMULA_BAD=0`,`ADMIN CHECK TABLE` 通过，index/table sample count 也一致。换句话说,**即便 downscale 已经准确落在“尾 worker 睡着但尚未上报错误”的窗口里,`MODIFY COLUMN` 仍然选择安全 rollback,没有出现 `ADD INDEX` 那种 silent publish / broken end-state。** 这条边界很值钱,因为它把 severe root 再收窄了一层:共享 `txnBackfillExecutor.adjustWorkerSize + tail-worker cancel + sendResult` 这些公共构件本身还不够,真正危险的更像还是 `ADD INDEX` 自己那条 result-acceptance / publish split。方法论上也新增一条很实用的规则: **当 failpoint 的 sleep 发生在可见日志事件之前时,live 控制动作应该锚在外部进度平台(`RowCount`/phase plateau),而不是锚在内部日志事件本身;日志更适合做 post-hoc 因果确认。**

**2026-07-11zzzh issue62531 这轮又补了两条很关键、而且彼此咬合的 live 证据:一条是否定 generic owner-side fix 假设,另一条是 `REPLACE` sibling 的强绿边界。** 先说最值钱的 live 因果试验:本地 UT 里已经证明 `pkg/executor/builder.go` 的 test-only failpoint `fixNewRowDecoderChangingColDefault` 能把同一份 raw row 的 changing slot 从 `""` 翻成 `"1"`;这次直接在 current owner `127.0.0.1:18182` 上 live 打开 `github.com/pingcap/tidb/pkg/executor/fixNewRowDecoderChangingColDefault=return(true)`，然后重放当前最稳的红格 `with-index=true, worker_mode=paired-follow, delete_session=fresh, delete_start=after-pause, prefill=120000, prefill_base=10000, worker_base=0, rows_per_op=200, hold=20s, post_release=20s, seed_base=1`。结果日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-fixrowdecoder-live-20260711.log` 仍然稳定 **RED**:`DDL released and finished` 之后继续命中 `worker 5 delete path hit issue62531 signature ... table_scan_executor.rs:467 ... missing data for NOT NULL column (offset = 2)`。这条结果非常关键,因为它把 current live root-cause 空间再收紧了一层: **generic `NewRowDecoder` / owner-side row-decode 规则本身不是 live severe red 的 load-bearing bridge**;它即使能解释本地 replay/value-flip,也不足以把 live 红格抹绿。换句话说,当前更像真的 still-live bridge 仍然更靠近 cop/table-scan executor 那一跳,而不是 owner 侧 generic row decoder。再说新的 sibling:在同一个 `beforeUpdateColumnBackfillApply=pause + disableLossyDDLOptimization=return(true)` live 窗口里,专门补了一条之前还没打过的 `REPLACE INTO` 单语句 probe。表结构是 `PRIMARY KEY(id) + UNIQUE KEY val0_idx(val0)`，pause 期由旧 session 执行 `replace into rows values (4, 1, 40)`，也就是显式走 **secondary-unique conflict -> 删旧行(handle=1) -> 插新行(handle=4)** 这一条最像“delete bad bridge + insert repair”组合的 sibling。结果日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-replace-single-live-20260711.log` 是干净 **GREEN**:`REPLACE_RC=0`,`DDL_ERR:` 为空,终态行集是 `2 2 20 / 3 3 30 / 4 1 40`,`USE INDEX/IGNORE INDEX(val0_idx)` 对 `val0='1'` 都只返回 `4`,`ADMIN CHECK` 通过。这个边界很值钱,因为它说明 **delete+insert 复合语义本身还不足以点燃当前 family**;即便它也会删旧唯一值再插入新 handle,只要不走当前 broad red 里的 scan/table-scan delete bridge,系统仍能自收敛。因此这轮把 issue62531 的 live selector 又压得更具体了:最稳的红不再是泛泛的 “任何带 delete 语义的 old-row lifecycle 都危险”,而是更像 **scan/table-scan delete bridge + concurrent row-lifecycle overlap**;同时本地 mechanism 里能翻值的 `fixNewRowDecoderChangingColDefault` 目前应继续被当作 replay/local bridge clue,还不能直接当 live fix hypothesis。方法论上也新增一条很实用的规则: **当某个本地 counterfactual 已经能翻动 replay payload 时,下一步一定要尽快做 live fix-validation;如果 live 红格不受影响,就要果断把它降级成“解释力很强但不是 live load-bearing bridge”的 clue,而不是继续围着它打转。**

**2026-07-11zzzg 这轮把两个容易混淆的方向重新分开了: common-reorg `ADD INDEX` 的 downscale severe root 目前没有自然扩到 `ADD PRIMARY KEY`,而 `issue62531` 这条 `MODIFY COLUMN` severe lane 则确认仍然活着,只是 current-master 上真正稳定的 red 更偏 broad overlap,不是先前压得太窄的 fixed-window 小格子。** 先说 common-reorg sibling: 在同一 owner `127.0.0.1:14101/18182` 上,把 `mockBackfillPostBatchErrForWorker=3 + sleep=10s + THREAD=1 downscale` 直接迁到 `ALTER TABLE ... ADD PRIMARY KEY(a) NONCLUSTERED`,`job 4527` 的 live 终态是 **`rollback done`**,表结构回到无主键,owner log 里连续多次出现 `mock backfill post-batch error on worker 3`、`backfill worker failed`、最后 `run add index job failed, convert job to rollback`;当前没有任何“silent publish / wrong-result”证据。这条边界很值钱,因为它说明前面 `ADD INDEX` severe hit 不是“所有共享 `typeAddIndexWorker` 的 DDL 都会被同样打坏”,真正危险的还是更窄的 result-delivery / publish split。再说 `issue62531`:本轮先按先前压出来的窄格直接重放 `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 的 `workers=16, rows_per_op=200, hold=15s, with-index=true, seed_base=1` 配置,新 schema `ai_native_issue62531_pool16_live_now` 结果是 **GREEN**;这说明那个 fixed-window 小格子并不适合继续被当作“当前稳定红格”。但把配置拉回 handoff 里后来验证过的 broad baseline:`with-index=true, prefill=120000, workers=16, hold=20s, post_release=40s, rows_per_op=0(random), seed_base=1` 后,`ai_native_issue62531_broad_live_now` 立即再次 live **RED**,日志直接命中 `worker 7 delete path hit issue62531 signature ... table_scan_executor.rs:467 ... missing data for NOT NULL column (offset = 2)`。随后又补了一轮同配置 `-after-red-oracle=true` 的 `ai_native_issue62531_broad_aftermath_now`,这次没有先撞到 red,最后整体 **GREEN** 收束。沿着“普通 reader 会不会单独受害”这条轴,又额外补了两个更细的 sibling:1) **`skip-delete=true + oracle-mode=select + oracle-workers=2` 在同一 broad baseline 下仍然 GREEN**(`ai_native_issue62531_insert_select_live_now`),说明“只有 insert overlap + prepared full-row select”还不足以单独点燃当前 severe lane;2) **把 `oracle-mode=select + oracle-workers=2` 重新挂回原本 nooracle 会红的 broad baseline 后,整格反而变 GREEN**(`ai_native_issue62531_broad_select_with_delete_now`, `oracle_select_ops=261`)。这条结果要非常小心地解读:它**不是**“并发 reader 已经安全”,更像是又一个 observer-effect 资产,说明 prepared full-row reader 自己会显著扰动时序/行像 materialization,甚至把原本 nooracle 的 red baseline 冷却掉。把这几枪放在一起,当前最诚实的更新是: **issue62531 family 仍是 current severe lane,但它比“workers=16 + rows_per_op=200 + hold=15s 就稳定红”更依赖 broad overlap 的工作负载形状与运行时调度;当前最稳的 live victim 仍然是 delete bridge,普通 prepared full-row reader 至少在当前 probe 形状下还不是稳定受害者;而 `ADD PRIMARY KEY` 则更像 `ADD INDEX` severe root 的安全 sibling boundary。** 方法论上新增两条很实用的规则:1) 当一个 red family 先被压成小格后,一定要保留一条更 broad 的 baseline replay。若小格 replay 变绿但 broad baseline 仍红,优先怀疑缺失的 controlling variable 是**workload entropy / overlap schedule / consumer timing**,而不是贸然宣布 family 消失。2) 对 execution-window severe lane,即使一个 oracle 在历史上命中过,也要持续防备它本身变成强扰动项;“同一 broad baseline nooracle=RED,加 prepared select oracle=GREEN” 这种 sibling 对比,本身就是一类一等的方法资产。**

**2026-07-11zzzf 我们刚在 common-reorg `ADD INDEX` 上打出了一条真正的 severe wrong-result bug,而且它非常贴合新 LOOP 里“运行时控制面动作 + in-flight worker 结果接纳”这条增强方向:把 txn-backfill worker 从 4 动态缩到 1 时,被移除 worker 的真实错误会被静默吞掉,DDL 仍然 `synced/public`,最终发布出一个缺行索引。** 这轮不是再问“pause/cancel 会不会继续推进”这种外围行为,而是直接把之前 PR-review 里一条很像 serious 的 seed 变成 live 实锤。我们先在 `/Users/bba/pc/tidb/pkg/ddl/backfilling.go` 临时补了一个 owner-only failpoint:`mockBackfillPostBatchErrForWorker` + `mockBackfillPostBatchErrSleepMs`,语义是“某个指定 backfill worker 在一批数据已经做完之后,先 sleep,再返回真实 error”。control 很干净:对 `job 4442` 在正常 4 worker 情况下只注入 `worker0` post-batch error,DDL 立即报 `ERROR 1105 mock backfill post-batch error on worker 0`,history 为 `rollback done`,owner log 也明确出现 `backfill worker failed`。但把同一套注入搬到 `job 4452`,改成 `worker3 + sleep 10s`,并在 `write reorganization` 期间执行 `ADMIN ALTER DDL JOBS 4452 THREAD = 1`,结果就完全变了:log 先在 `14:16:23 UTC` 记录 `adjust ddl job config success current worker count=1`,随后 `14:16:31 UTC` 明确打出 `mock backfill post-batch error injected workerID=3`,但后面**没有** `backfill worker failed`,反而紧接着是 `backfill workers successfully processed`、`run reorg job done handled rows=30269`,`finish DDL job ... state:synced`。用户层后果非常硬:`ADMIN CHECK TABLE t` 直接报 `ERROR 8223 data inconsistency`,而且普通查询已经错了:同一张 32768 行表上,`COUNT(*) IGNORE INDEX(idx_a)=32768`,`COUNT(*) FORCE INDEX(idx_a)=30301`,`COUNT(*)` 也变成 `30301`;具体 witness `a=5676` 在 table scan 下有 1 行,但走 `idx_a` 或默认 plan 都返回 0。`EXPLAIN` 还能看到普通 `COUNT(*)` 已经选了 `IndexFullScan(idx_a)`。当前最像真的代码级闭环也很清楚:`adjustWorkerSize()` 缩容时保留 `b.workers[:1]` 这类**切片前缀**,并 cancel 尾部 worker;这次 live 里先自然跑完的是 `worker0/1/2`,真正还在干活的是 `worker3`,于是 shrink 实际上保留了一个已空闲的前缀 worker,反而 cancel 了仍在跑的 tail worker。后者在 sleep 结束后虽然确实产出 error,但 `sendResult()` 先看到 `w.ctx.Done()` 就直接把结果丢掉,collector 最终只看到 channel 干净关闭,于是错误地记成 `backfill workers successfully processed`。所以这条不是泛泛 race,而是很具体的: **动态缩容把“错误 worker 的结果必须被接纳/传播”这个证明义务打破了,系统因此错误地相信 backfill 已成功收敛,于是发布了不完整索引。** 后续这轮又专门补了更系统的 safe-retry sibling,结果同样很值钱: `job 4487`(pre-send synthetic error on tail workers) 和 `job 4492` 以及 batch `4497/4502/4507`(post-batch error + before-send sleep on tail workers) 都在 downscale 后命中了 tail-worker error,日志里能看到 `mock backfill ... injected`、`backfill worker failed`、`ErrCount=1`,但最终 `COUNT(*)/IGNORE/FORCE` 全绿、`ADMIN CHECK TABLE` 通过,只是 `RowCount` 会膨胀到 `23w+`。这说明 current root 还比“任何 canceled tail error 都会坏”更窄: **真正 load-bearing 的是 `sendResult` 那个 `ctx.Done()` vs `resultCh <- result` 的竞争窗口。** 在这些 sibling 里,`send` 这边赢了,系统走了可恢复 retry-safe path;而在 `job 4452` 的 severe hit 里,更像是 `ctx.Done()` 这边赢了,于是 error/result 被静默丢掉。方法论增量也更扎实了:1) 这不是纯源码审查或 SQL 小矩阵能自然长出来的,而是“先从 PR-review/源码里找到 serious obligation,再让 AI 改 TiDB 做定点 error 注入,再用强 oracle 看 publish 后 end-state”才命中。2) 对 reorg/txn 这类并发模块,**动态控制面动作**(`thread downscale`,`pause`,`cancel`,`owner handoff`)本身就该被视为一类一等 `D_dim`,不能只测静态功能路径。3) 命中 severe 之后,不要急着泛化成一个大而空的 family,要马上补 safe-retry sibling,把 root 从“downscale 吞错”继续压成“result-delivery race + retry/publish split”。当前这条建议尽快单列草稿并入 bug 库,因为它已经不是 moderate wrong-error,而是用户可见的 published wrong-result / data consistency break。**

**2026-07-11zzzb 我们刚把 distributed `ADD INDEX` 这条 runtime-asset-loss candidate 又收紧了一层:现在已经能证明,不需要删整个 local engine 目录,在 `SetTSBeforeImportEngine` 之后只删掉单个 `000004.sst` 也足以把执行 subtask 的 TiDB front 直接打掉。** 这轮专门把 live topology 收成了更干净的形状: `4001` 用 custom `/fp` 成为 DDL owner 和唯一执行节点(`tidb_max_dist_task_nodes=1`), `4003` 只保留作 failover survivor。随后在 `job 4282 / global task 420001` 上,对 `127.0.0.1:14001` 发起 plain distributed `ALTER TABLE t_single_sst ADD INDEX idx_b(b)`,在 `pauseAfterSetTSBeforeImportEngine=1*pause` 卡住后,只删除 `/tmp/fp-survivor-20260711/tmp_ddl-4001/4282/09ae5021-ec10-5ca9-8a0d-fbeb2e371e16/000004.sst` 再放行。用户层结果非常硬:前台 `ALTER` 立刻报 `ERROR 2013 (HY000): Lost connection to MySQL server during query`,随后 `4001` 本身就从 pod 进程表里消失;这说明这条 candidate 不只是“目录级 chaos 太粗”,而是**最小到单 SST 文件的运行时资产丢失**就能触发同一个 `MustExist -> Fatalf -> os.Exit(1)` 形状。后半段也看清了: `4003` 在约 35 秒后接管 owner,并以同一个 `task-id=420001 / engineUUID=09ae...` 重新跑 read-index/import,最终 `job 4282` 在 `2026-07-11 13:21:52 UTC` `synced`,`idx_b` 成功出现。这里还有一条很重要的方法论修正:中途一度看起来像“failover 后仍然挂死”,但复盘发现是**我们自己在 4003 上残留了同一个 pause failpoint**;清掉 `23390` 上的 `pauseAfterSetTSBeforeImportEngine` 后,`4003` 立刻继续 `import start -> import engine success -> run subtask completed`。所以这轮最值钱的新增不是一个全新 root cause,而是两个更硬的结论:1) 这条 availability candidate 的必要扰动已经被压到**单文件级**;2) 在 runtime-asset-loss / failover 类实验里,AI 必须把**所有候选 executor 的 failpoint state 审计**纳入标准流程,否则很容易把实验残留误判成产品挂死。**

**2026-07-11zzza 我们刚在 distributed `ADD INDEX` lane 上压出一条新的 high-value availability candidate,而且它正好验证了新 LOOP 里“运行时资产扰动 + owner failover 观察”这条增强能力:如果在 `SetTSBeforeImportEngine` 之后、真正 `import` 开始前把 local engine 目录拿掉,执行 subtask 的 TiDB front 会直接退出,前台 `ALTER TABLE ... ADD INDEX` 连接丢失;但另一个 owner 会在约 1 分钟后把任务捡起来并最终补完。** 这轮不是继续靠 synthetic error string 打 retry classifier,而是用 commit-matched failpoint build 上已经存在的 natural pause hook `pkg/ingestor/ingestctrl/pauseAfterSetTSBeforeImportEngine`。在 testbed `8220955` 的 failpoint owner `127.0.0.1:14000`/`10080` 上,把 owner 先切到 `4000`,挂 `pauseAfterSetTSBeforeImportEngine=1*pause`,然后对 2w 行表直接跑 plain distributed `ALTER TABLE ... ADD INDEX idx_a(a)`。等 owner log 走到 `set ingest ts before import` 并在 pause 窗口内生成真实 engine 目录后,外部删掉 `/tmp/fp-reload-20260711d/tmp_ddl-4000/<job>/<engine_uuid>` 再放行。结果两次 live run 都非常一致:前台 `ALTER` 会立刻报 `ERROR 2013 (HY000): Lost connection to MySQL server during query`;owner log 在 `import start` 后停在真实文件缺失 `orig err: open .../000004.sst: no such file or directory / list err: open .../<engine_uuid>: no such file or directory`;随后 `4000/10080` 监听直接消失,说明不是普通 SQL error return,而是执行 subtask 的 TiDB 进程本身被打掉。第一次 `job 4172 / task 330001` 在约 `11:49:06 UTC` 自愈成功;第二次 `job 4179 / task 360001` 更能看清恢复轨迹:约 20 秒后 `4001` 已重新成为 DDL owner,表上暂时还没有 `idx_a`,当前 `mysql.tidb_background_subtask` 里 task 仍是 `running`,而且还绑着 stale `exec_id=10.200.16.101:4000`;再过约 1 分钟,history 里 task `360001` 以 `exec_id=10.200.16.101:4001` 成功收尾,DDL `4179` 在 `11:54:17 UTC` `synced`。现在 source chain 也基本闭环了:TiDB 打开 local engine Pebble DB 时没有自定义 `Logger`;Pebble 读取本地对象走 `MustExist: true`;一旦目录已被删掉,`MustExist -> Fatalf -> os.Exit(1)` 会打印与现场完全同形状的 `orig err/list err`。同时 live pod topology 也解释清楚了为什么 k8s 侧不显 pod restart: `fp-tidb` 的 PID1 只是 `sleep infinity`,所以 `4000` 这条 TiDB 进程死掉后 pod 仍会保持 `Running`。这条很值钱,因为它不是旧 loop 擅长的“纯 SQL proof obligation 红格”,而是**runtime asset loss -> import path fatal -> TiDB process exit -> failover self-heal** 这一类更接近 2/3 阶段的系统级红点。对方法论的直接增量是:以后在 `DXF / ingest / txn` 这类 stateful 模块上,AI 不该只停在源码 proof obligation 和 SQL 小矩阵,还要主动问一句:`系统在已经 persisting 过关键 state 之后,如果运行时资产突然缺失,是优雅 fail、错误分类重试、还是直接把 owner/front 打掉?` 这条当前先记为 high-value candidate,草稿已写 `/Users/bba/pc/ai-native-dist-addindex-local-engine-loss-crash-draft.md`。**

**2026-07-11zza 我们刚把 `delayForAsyncCommit` family 正式抬成了一个新的 natural severe confirmed bug,而且这次不是 failpoint 注入,是 current-master front 上的真实红绿分叉。** 在 testbed `8220955` 的 current-master front `127.0.0.1:14001` 上,直接重跑 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go` 的最小主路径格:`plain ADD INDEX + async commit + basic(insert -> update) + no-pause + same-start + hold=0ms`。结果今天的现场仍然很干净: `metadata_lock=OFF` 时,新日志 `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-mdloff-rerun-20260711-1828.log` 记录 `AFTER_HOLD ddl_status=running` 后事务直接报 `Error 8028 (HY000): Information schema is changed`;而完全同形状、只把 `metadata_lock=ON` 的对照 `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-mdlon-rerun-20260711-1828.log` 立即 **GREEN**,probe 会继续跑 `ADMIN CHECK TABLE`、index/table differential 和 exact-row oracle(`1:10,2:2`)。这条之所以值钱,不只是因为它是 natural red,还因为源码和测试自己已经给了很强的 product contract: `pkg/ddl/ddl.go:1300-1323` 明写 `delayForAsyncCommit()` “provides a safe window for async commit and 1PC to commit with an old schema”,而 `tests/realtikvtest/pessimistictest/pessimistic_test.go:2150-2240` 里两个被 skip 的 `TestAsyncCommitWithSchemaChange` / `Test1PCWithSchemaChange` 也明确期待同类 `ADD INDEX` 场景下事务成功、索引 key 被 amend。也就是说,这次问的不再是“某个异常会不会被错分流”,而是**代码自己声称 safe 的路径,在 current runtime 上还能不能真的 safe**。当前最像真的 root-cause clue 也非常具体:`pkg/kv/option.go` 仍保留 `SchemaAmender` 选项注释,但 current transaction setup(`pkg/sessiontxn/isolation/base.go:511-516`,`pkg/store/driver/txn/txn_driver.go:229-263`) 只明显接了 `SchemaLeaseChecker + InfoSchema + EnableAsyncCommit/Enable1PC`;全树搜索里也看不到任何 `SetOption(kv.SchemaAmender, ...)`。因此,MDL-off 这条 safe-window path 很像已经从“旧 schema 仍可安全 commit/amend”的 contract,退化成了“最后只剩 schema lease check,于是直接 `ErrInfoSchemaChanged`”。这条已入远端 `found_bug id1440001`(`severity=high,status=confirmed,root_cause_id=async-commit-schema-change-safe-window-broken`),远端计数更新到 `87 confirmed surfaces / 64 distinct root causes`。对 LOOP 的增量也很直接: **源码注释里的 runtime-safety claim + 被 skip 的 expected-success test,本身就是高价值 proof obligation。只要能在 live current-master 上做出一个同形状的 natural RED/GREEN sibling differential,它的质量往往比一条新的 failpoint 注入还高。**

**2026-07-11zz 我们刚在同一条 distributed `ADD INDEX` / `SetTSBeforeImportEngine` live lane 上补出第二个真正 high-value 的 DDL liveness root，而且它不是 `id1350002` 的简单换皮: 这次红的不是 `engine-not-found` 这种 source-native fundamental，而是** `context deadline exceeded` **这种“本来就该 retry”的 runtime timeout；问题在于 DXF 外层根本没有 terminal retry budget。** 在 testbed `8220955` 的 commit-matched failpoint owner 上，同点位小矩阵非常干净：`mockAINativeSetTSBeforeImportEngineErr=1*return("context_deadline_exceeded")` 的 one-shot control 是 **GREEN**(`job 4002`, `task 300007 -> succeed`)，而 persistent `return("context_deadline_exceeded")` 则把 `ALTER TABLE ... ADD INDEX` 卡成 **RED**: `job 4007` 在 `running/write reorganization` 持续超过 90s，DXF task `300008` 一直 `running/step=1`，前台 `ALTER` 不返回；一旦外部 `DELETE` 掉 failpoint，`task 300008` 先转 `succeed`，`job 4007` 在约 2 秒内 `synced`。owner log 证据也比上条更像 production severe: `task-id=300008` 在 `2026-07-11 10:16:11` 到 `10:17:39 UTC` 之间累计打出 **247** 条 `meet retryable error`，重复模式稳定是 `build add index local storage operators -> set ingest ts before import -> import error "context deadline exceeded" -> run subtask failed -> meet retryable error`。这说明新的 reusable selector 不能再只写成“fundamental error 被错放进 retry loop”(S25)，还要补出一条**更隐蔽但更危险**的新 lane:`S26_DXF_RETRYABLE_RUNTIME_NO_RETRY_BUDGET`。它的核心不是 retryability 判断本身错了，而是 **系统把“retryable”偷换成了“可以一直 running/rerun,无需预算、无需升级、无需终态”**。更关键的是，这条 severe red 还有一个很强的 source-side contrast: 旧 ingest 层本身已经有 `MaxWriteAndIngestRetryTimes=30` 和 backoff 上限，但 DXF `task_executor` 对 retryable subtask 只会“保持 running，不标 failed”，于是 lower-layer bounded retry 之上又叠出一个 unbounded outer loop。这个 family 已经写入远端 `found_bug id1410001`(`severity=high,status=confirmed,root_cause_id=dist-addindex-retryable-timeout-unbounded-loop`)；它对 LOOP 的增量也很实在: **以后在 retry/liveness lane 上，不能只问“错误有没有被错分成 retryable/fatal”，还要继续追问“即便 retryable, 它有没有 budget / escalation / terminal action?”。**

**2026-07-11zzd owner/topology severe lane 这轮又向 terminal phase 深挖了一层,而且结果是一个高价值 `NEGATIVE_BOUNDARY`:即使把 `ADD INDEX` 精确卡在“index 已经可见、前台 DDL session 还没真正返回”的 late terminal window,当前实现对 owner handoff 仍然能自收敛。** 这次没有再用粗 pod chaos,而是直接利用 live failpoint `pkg/ddl/create-index-stuck-before-ddlhistory`。先把 DDL owner 明确切到 failpoint build `4000`,再只在 owner `127.0.0.1:18080` 上挂住 `create-index-stuck-before-ddlhistory=return("/tmp/ai_native_ddlhistory_release2")`,然后从非 owner front `127.0.0.1:14001` 跑最小 `ALTER TABLE ... ADD INDEX idx_c(c)`。观测非常干净:表只有 1w 行时,`tick=3` 就已经看到 `information_schema.statistics` 里 `idx_c` 可见,同时前台 `ALTER` 连接仍然挂着,说明我们确实 pin 到了“schema/index 已 publish,但 terminal history/return path 还没完全收尾”的小窗。随后在**不释放旧 owner failpoint** 的情况下,直接 `POST /ddl/owner/resign` 让 owner 从 `4000 -> 4001`;结果新 owner 在约 3 秒内把 DDL 自己收完,前台 `ALTER` 正常返回 success,`ADMIN CHECK TABLE`、table/index count 都绿。这个结果比早期 `beforeCreateLocalBackend` / `resignAfterFlush` 两条绿边界更强,因为它说明 handoff-safe 的范围已经延伸到了**terminalization after publish**。方法论收获也很具体:1) owner/topology live harness 现在不只会打 early/flush phase,也已经能打 late terminal phase;2) 在这种 live severe lane 上,要先确认 **injection owner 与 phase owner 是否同侧**,否则会出现“owner 在 4001,但 `/fail/` 只挂在 4000”这种假 blocker。当前判断:terminal handoff 这条子窗口先记为 `NEGATIVE_BOUNDARY`,后续若继续追 `OWNER_TOPOLOGY_HANDOFF`,更值得去碰的是 publish 前后的 result persistence / rollback-resume / history-write 之外的更尖恢复点,而不是重复这条已证绿的 late window。**

**2026-07-11zzc2 我们刚把 `7r MODIFY COLUMN transient connection family` 从 local-only 真正 lift 到了 live,而且顺手补出一条非常关键的 negative boundary:当前 `common reorg` 的 terminal 行为和 `DXF` 不一样,同样 persistent retryable fault 不会把 `ADD INDEX` 卡成 unbounded running,而是直接 rollback。** 这轮先把 failpoint owner lane 重新校准了一次:发现 pod 里手工拉起的 `/tidb-server` 虽然能吃 `tikvclient/beforePrewrite`,但对 `pkg/ddl/mockBackfillRunTransientErr` 这种 worker-level semantic failpoint 基本不命中。于是我们回到 `/private/tmp/fp-build-5c9198`,把 main repo 里已经本地验证过的 `mockBackfillRunTransientErr` family 补进 `pkg/ddl/backfilling.go`,重新 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tidb-server-fp ./cmd/tidb-server`,流式回灌 pod 内 `/fp`,并让 `4003/10086` 成为新的 custom failpoint owner。新的 owner lane 上,live semantic split 很干净:1) `mockBackfillRunTransientErr=1*return("net_conn_reset")` 时,`ALTER TABLE ai_native_live_retry.t_add_worker_once ADD INDEX idx_b(b)` 仍然 **GREEN**(`job 4262`, `state=synced`, `ADMIN CHECK TABLE` 绿,`SHOW CREATE TABLE` 可见 `KEY idx_b(b)`);2) 同一个 one-shot shape 下,`ALTER TABLE ai_native_live_retry.t_mod_worker_once CHANGE COLUMN b b varchar(16)` 直接 **RED**:用户层 `ERROR 1105 (HY000): read tcp: read: connection reset by peer`,history `job 4265` 为 `rollback done`,表结构保持原 `b int`。更关键的是,我们又专门追了一格 persistent live terminal oracle:对同一 owner lane 打 `mockBackfillRunTransientErr=return("net_conn_reset")`,`ADD INDEX` 并没有像 DXF `S26` 那样卡成 `running` 等外部清 fault,而是很快直接向用户返回同样的 `connection reset by peer`,history `job 4268` 也是 `rollback done`;同样,bridge-level classifier failpoint `mockDDLAddIndexClassifierErr=return("driver_bad_conn")` / `return("context_deadline_exceeded")` 的 persistent case 也都分别在 `job 4253 / 4259` 上直接 `rollback done`,而对应 one-shot `driver_bad_conn` 仍然 **GREEN**(`job 4256`, `synced`)。这条 negative boundary 很值钱,因为它把一个很容易“想当然迁移”的 selector 挡住了: **DXF 的 `retryable but no terminal budget` 并不会自动推广到 common reorg; common reorg 对 persistent bridge/worker retryable fault 当前更像 finite rollback path,不是 unbounded running path。** 方法论上的增量也很明确:1) live semantic injection 先要做 **binary capability audit**;HTTP 能 `PUT` 不等于当前 owner binary 真含该 failpoint,必要时必须回到 custom `/fp` worktree 重编。2) 当同一 retryable family 在不同框架里都能 one-shot RED/GREEN split 时,还要继续加一格 **persistent terminal oracle** 去看是 `hang/rerun`、`finite rollback` 还是 `self-heal`。这一步直接决定 selector 能不能跨框架复用。当前结论: `7r` 这条 family 已经在 live 上站住了,但它目前更像 common reorg availability differential/finite rollback family,而不是新的 `S26`-style hang bug。**

**2026-07-11zzc 我们刚把 `MODIFY COLUMN` 一条看起来很像 severe 的 old-schema overlap 邻域,用强 end-state oracle 正式打成了 `NEGATIVE_BOUNDARY`:当前 master 至少在 `MODIFY ... NOT NULL` 与 `bigint -> tinyint` 这两个 proof obligation 上,并没有出现“DDL 成功但旧 schema 脏写偷偷混进来”的 silent integrity break。** 这轮新增探针 `/Users/bba/pc/ai-native-probes/modify_column_async_commit_not_null_probe.go`,不再只盯 `Error 8028` 这种 availability 红,而是直接问更尖的 severe 义务: **DDL check 完成之后,old-schema async commit / 1PC 能不能在 DDL 终态成功前偷偷塞进新规则已不允许的数据?** 结果 split 很干净。对 `ALTER TABLE ... MODIFY a INT NOT NULL`,如果把旧 schema 事务一直 hold 到 DDL 完全 finished 再放行,事务直接报 `Error 8028`；如果等到 probe 观测到“新 schema 已经开始拒绝 NULL”这个 marker 再提前放行,结果是 **DDL 报 `ERROR 1138 Invalid use of NULL value` 回滚**,而事务成功,最终 schema 保持 nullable、表里确实留有 `NULL`。`1PC` 与 `UPDATE ... SET a=NULL` sibling 也都是同一保护性 split。对 `ALTER TABLE ... MODIFY a TINYINT NOT NULL`,把旧 schema 事务里的 `512` 延迟到“新 schema 已经开始拒绝越界值”之后再放,结果同样是 **DDL 报 `ERROR 1265 Data truncated` 回滚**,事务成功,最终 schema 保持 `BIGINT`。这条负样本非常值钱,因为它不是“没打中窗口”,而是已经把最危险的 old-schema overlap 明确 pin 到了“要么 txn fail,要么 DDL fail,不会 silent mixed-success”。方法论增量也很直接: **当某条 severe 猜想已经具体到“检查过的 P 是否还能支撑 fast path 的 Q”时,最值得优先构造的不是更大的 chaos,而是这种能区分 `txn fail / ddl fail / mixed-success` 的小矩阵强 oracle。** 当前判断:这条 `modify-column safe-window` 邻域先收口为 `NEGATIVE_BOUNDARY`,后续除非出现更窄 phase hook,否则不要继续在这里盲堆 owner/topology chaos。

**2026-07-11zzb owner/topology severe lane 这轮也拿到了一个很关键的能力升级:live testbed 上的真实 owner handoff 现在已经不是“推测可能发生”,而是可被 failpoint 精确触发、可被 `ADMIN SHOW DDL` 观察确认、可被现成强 oracle harness 复用的执行通道。** 我们在 testbed `8220955` 的 failpoint owner front(`127.0.0.1:14000`,status `127.0.0.1:18080`)验证了三组 live failpoint 都在当前二进制里可设:`pkg/ddl/ownerResignAfterDispatchLoopCheck`,`pkg/ddl/ingest/beforeCreateLocalBackend`,`pkg/ddl/ingest/resignAfterFlush`。随后直接复用 `/Users/bba/pc/ai-native-probes/add_index_owner_fault_oracle_probe.go` 作为 terminal oracle harness,让真正的 owner 切换由 failpoint 驱动而不是 probe 自己“伪造成功/失败”。结果已经能实锤 owner 在 `4000 <-> 4001` 之间来回迁移:对 early-ingest schedule(`beforeCreateLocalBackend + ownerResignAfterDispatchLoopCheck`)与 flush-after schedule(`resignAfterFlush + ownerResignAfterDispatchLoopCheck`),`ADMIN SHOW DDL` 都观察到了 owner address 在 `10.200.16.101:4000` 与 `10.200.16.101:4001` 之间真实切换;而对应 `ADD INDEX` job 终态都保持 `synced/public`,`index visible + exact count + ADMIN CHECK TABLE` 全绿。这个结果本身不是新 bug,但它把 owner/topology severe 搜索从“外部 pod bounce/PD chaos 猜窗口”升级成了**phase-aware live owner handoff harness**。对 LOOP 的增量是:以后这条 lane 的重点不该再是“能不能造成一次 handoff”,而应继续收窄到**在哪个 phase handoff 才会让 stale result / checkpoint / result-acceptance 变成真正的 user-visible red**。当前最值得继续追的不是早期 backend-create 和 flush-after 这两条已证绿 schedule,而是更靠近 reorg result acceptance / checkpoint merge / rollback-resume 的 sharper handoff 子窗口。**

**2026-07-11zw 这轮把 `delayForAsyncCommit` family 真正抬上了 live testbed：`plain ADD INDEX` 主路径在真实集群上已经不再只是本地 natural-red，而是有了稳定的 `MDL OFF -> RED / MDL ON -> GREEN` 小矩阵；同时也证明了“显式事务壳 vs autocommit statement 壳”必须作为独立 `D_dim` 记录。** 这轮先没有继续扩 sibling，而是把现成的 strongest lane 往用户面再抬一级。先做 live 入口：在 testbed `8220955` 的 namespace `testbed-tps-8220955-1-213` 里，`tc-tidb` service 没有 endpoints，于是在 `fp-tidb` pod 内临时拉起了一个 `tidb-server` front(`-store=tikv -path=tc-pd:2379 -P=4001 -status=10082`)，再本地转发到 `127.0.0.1:14001/18082`。第一条很重要的负样本是：之前本地已经压出的 `ADD UNIQUE INDEX + async commit + insert1` single-op natural red，并没有自动抬成 live 最小格；直接用 live DSN 跑这格(`/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go -ddl-kind add-unique-index -txn-kind async-commit -txn-shape insert1 -pause-prewrite=false -hold=0ms -ddl-start-gap=0ms`) 返回 **GREEN**。这和同轮 autocommit statement 收缩结果一致：`stmtinsert1/stmtupdate1/stmtinsert2/stmtupdate2` 在 `add-unique-index` 上都没有点亮红格，说明当前 family 的最强用户面还不是“任意单条 statement 都会坏”，而更像**explicit transaction shell 仍然是当前 selector 的真实组成部分**。真正把严重性抬上去的是 live 小矩阵：对 `plain ADD INDEX + basic(insert -> update)`，async commit 轴在 `MDL OFF + no-pause + hold=0ms` 下，用 `ddl-start-gap=0/1/2/5/10ms` 每格复跑两次，证据文件 `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-gap-matrix-mdloff-20260711.log` 是 **10/10 RED**；对应 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-gap-matrix-mdlon-20260711.log` 是 **6/6 GREEN**。这说明在真实集群上，plain `ADD INDEX` 的 mainstream 主路径已经有**稳定 live availability red band**，而且 `MDL ON` 仍然是很强的 protective sibling。`1PC` 轴也被抬上来了，只是更 near-boundary：`/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-1pc-basic-gap-matrix-mdloff-20260711.log` 在 `gap=0/5/10ms` 共 6 次里命中 **3 RED / 3 GREEN**，而 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-1pc-basic-gap-matrix-mdlon-20260711.log` 是 **4/4 GREEN**。因此，当前 family 的最强总结要改写成两层：1) **严重 bug 已经坐实到 live testbed 上的 plain `ADD INDEX` 主路径**，其中 async commit lane 现在最适合 issue/修复验证；2) **不要把 local single-op red 直接写成 live 最小用户面**，也不要把显式事务壳默认压扁成 autocommit statement。对后续方法论最值钱的结论是：当 local natural red 已经命中主路径时，下一步不是盲目扩更多 sibling，而是先做一个只扫单一时间维度的 live 小矩阵，把“偶然红格”升级成“可重复 live 红带”，再回头收窄真正的用户面。**

**2026-07-11zv `delayForAsyncCommit` family 这轮又把 mainstream sibling 往外扩了一层，而且结果不是简单重复 `ADD INDEX/ADD UNIQUE INDEX`，而是给出了一套新的 protocol split：`ADD PRIMARY KEY` 在 current master 上对 `async commit` 明显更宽，几乎已经压到 single-op natural red；而 `1PC` 仍然更像多步事务 family。** 这轮继续只用 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`，把现成的 `basic/basicrev/insert1/insert2/update1/update2` shape 直接接到 `ddl-kind=add-primary-key`。结果有三层很值钱。第一层，`MDL OFF + same-start + no-pause` 下 plain `ADD PRIMARY KEY` 本身已经确定属于同一条 severe availability family：`basic` 在两个协议上都直接红(`/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，而 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-basic-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-basic-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 都是 **GREEN**。第二层，**async commit 这条线现在非常宽**：单操作已经足够点亮。`insert1` 在 `/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-insert1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 是 **RED**，其 `MDL ON` 对照绿；`update1` 在 `/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-update1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 也是 **RED**，`MDL ON` 对照绿；`insert2` 仍红(`/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，`update2` 初跑有一个 GREEN outlier，但复跑两次 `/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-commit-update2-cross-hold0ms-gap0ms-nopause-mdloff-rerun1-20260711.log` 与 `...rerun2...` 都回到 **RED**，对应 `MDL ON` 对照也绿。也就是说，对 async commit 来说，`ADD PRIMARY KEY` 这条 sibling 已经不像 plain `ADD INDEX` 还需要“至少两步 key-affecting 操作”那样谨慎；它更像**单个并发 DML 就足以进入 natural availability red**。第三层，`1PC` picture 更细：它没有被压成单操作 red，但比 plain `ADD INDEX` 更宽。`insert1` `/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-insert1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 仍是 **GREEN**，`update1` `/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-update1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 也是 **GREEN**；但 `insert2` `/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `update2` `/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-update2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 都是 **RED**，对应 `MDL ON` 对照绿。顺序维度在 `1PC` 上反而没有 plain `ADD INDEX` 那么干净：`basicrev(update -> insert)` 一次 GREEN、一次 RED、再一次 GREEN(`/Users/bba/pc/ai-native-assets/logs/add-primary-key-1pc-basicrev-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`...rerun1...`、`...rerun2...`)，说明它更像 near-boundary phase-jitter，而不是稳定的 order selector。把三条 mainstream sibling 合起来，当前 family 的最强总结已经不是“单一主路径上有个 availability bug”，而是更强的：**`ADD INDEX` / `ADD UNIQUE INDEX` / `ADD PRIMARY KEY` 三条 mainstream DDL sibling 已经都能在 `no-pause + same-start + MDL OFF` 下长出自然红格；但最小义务和协议分叉并不相同。`ADD UNIQUE INDEX` 已能长出 sibling-specific single-op red，`ADD PRIMARY KEY` 对 async commit 更宽，`ADD INDEX` 则更适合用最小两步事务与顺序维度解释。** 这组差异本身就是高质量 selector 资产，因为它说明后续 issue 包和 fix 验证不能再只拿一个 sibling 代替全 family。**

**2026-07-11zu `delayForAsyncCommit` family 这轮沿 mainstream sibling 又长出一个更强的 severe 方向：`ADD UNIQUE INDEX` 并没有沿 plain `ADD INDEX` 那套“至少需要两步组合义务”走，而是已经在 current master 上长出了** `single-op natural red` **，而且是明显的 protocol-specific split。** 这轮没有换 lane，只是把刚刚在 plain `ADD INDEX` 上打磨出来的 `insert1/insert2/update1/update2/basic/basicrev` 继续横向压到 `add-unique-index`。结果比预想更强。第一层结论是：`add-unique-index` 当前至少有两格**稳定单操作 red**。`async commit + insert1 + no-pause + same-start + MDL OFF` 在 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-commit-insert1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-commit-insert1-cross-hold0ms-gap0ms-nopause-mdloff-rerun-20260711.log` 都是 **RED**，而 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-commit-insert1-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 是 **GREEN**；`1PC + update1 + no-pause + same-start + MDL OFF` 在 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-1pc-update1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-1pc-update1-cross-hold0ms-gap0ms-nopause-mdloff-rerun-20260711.log` 都是 **RED**，对应 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-1pc-update1-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 是 **GREEN**。这意味着相对 plain `ADD INDEX` 而言，**unique-key amend 语义会把当前 selector 再往前压到单个 key-affecting DML 就够危险**，只是不同协议点亮的单操作不一样：async 更容易被单次 insert 点亮，1PC 更容易被单次 update 点亮。第二层结论是：两步事务格子现在更像是“宽红带”，但没有 plain `ADD INDEX` 那么容易抽出一个稳定的顺序 selector。`basic(insert -> update)` 与 `basicrev(update -> insert)` 在 `MDL OFF` 下对两个协议都出现了 red/green 混合复跑：例如 `async + basic` 一次 RED、一次 GREEN、一次 RED，`1PC + basicrev` 也一红一绿。这说明 `add-unique-index` 在当前 `hold=0ms` 的自然 lane 上更像**near-boundary phase-sensitive family**，不应把一次红格直接写死成事务内部顺序结论。第三层结论是，即使 order 维度暂时不稳，某些 wider cells 还是已经足够硬：`update2` 在两个协议上都 RED，并且 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-commit-update2-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-unique-index-1pc-update2-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 都是 **GREEN**；`insert2` 则出现 protocol split：`async commit` 绿(`/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-commit-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，`1PC` 红(`/Users/bba/pc/ai-native-assets/logs/add-unique-index-1pc-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，其 `MDL ON` 对照也绿。把这些格子和 plain `ADD INDEX` 的 current picture 放在一起，当前 family 的 selector 已经明显升级成**sibling-specific protocol selector**：plain `ADD INDEX` 目前更像“1PC 对旧-key rewrite/语句顺序更敏感；async 需要至少两步 key-affecting 操作”；而 `ADD UNIQUE INDEX` 则已经压成“async 可被单次 insert 点亮，1PC 可被单次 update 点亮”，同时两步格子形成更宽的 phase-sensitive 红带。对 severe 目标来说，这条结果很值钱，因为它说明当前 bug family 不只是 plain `ADD INDEX` 的一条尖针，而是**single-index mainstream sibling 上已经存在更强的自然 availability surface**。**

**2026-07-11zt 这轮把 plain `ADD INDEX` 主路径又往前收紧了一层，而且第一次压出了一个很关键的 `D_dim`: 在 current master 的 `no-pause + same-start` 自然红格里，`1PC` 对事务内部语句顺序是敏感的；`async commit` 则更像“只要有两步 key-affecting DML 就容易红”。** 这轮继续留在 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`，但不再扩 DDL kind，而是对已经命中的 plain `ADD INDEX` 主路径做更细的事务内最小化。新增了 `txn-shape=insert2|update2|basicrev`：`insert2` 表示两次单独 `INSERT` 新 key，`update2` 表示两次单独 `UPDATE` 旧 key，`basicrev` 表示把原来 `basic(insert -> update)` 倒过来变成 `update -> insert`。结果很有信息量。先看 `1PC`：plain `ADD INDEX + basic(insert -> update) + no-pause + MDL OFF` 之前已经稳定 RED，这轮继续确认了它不是偶发；但同一 final rowset 的 `basicrev(update -> insert)` 变体 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-basicrev-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 和 `...basicrev...rerun-20260711.log` 都是 **GREEN**，`MDL ON` 对照也绿。这说明对 `1PC` 来说，**不是“同一事务里既有 insert 又有 update”这个集合事实本身就足够，而是事务内部顺序也属于当前 family 的真实 `D_dim`。** 继续看另外两个拆分格：`insert2` 在 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 是 **GREEN**，而 `update2` 在 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-update2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 是 **RED**，其 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-update2-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 是 **GREEN**。再加上之前已经知道的 `insert1` / `update1` 都绿，当前 `1PC` 的 selector 已经能写得更窄：**plain `ADD INDEX` 的自然红格不是“任何两步事务都危险”，而更像“事务后段发生旧 key rewrite，或连续旧 key rewrite，会把它推红；纯新 key create 组合还不够，`update -> insert` 也明显更宽。”** async commit 轴上 picture 又不一样：`basic(insert -> update)` 已经多次稳定 RED，`insert1` 和 `update1` 都绿，但新的 `insert2` `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-insert2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`update2` `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-update2-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`basicrev(update -> insert)` `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-basicrev-cross-hold0ms-gap0ms-nopause-mdloff-rerun2-20260711.log` 与 `...rerun3...` 也都 RED；虽然第一条 `basicrev` 初跑曾有一个 GREEN outlier(`/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-basicrev-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，但后续复跑两次都回到 RED，`MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-basicrev-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 仍是 **GREEN**。所以对 async commit 来说，当前 live lane 更像：**单个 key-affecting DML 还不够，但一旦同一事务里叠出两步 key-affecting 操作，不论是两次 insert、两次 update、还是 update 后再 insert，都已经很容易进入 natural availability red。** 这轮最值钱的方法论结论是两个。第一，**mainstream natural-red lane 里，事务内部顺序本身就是值得单独拆的 `D_dim`**，尤其当 final rowset 一样时，这个维度非常能说明问题。第二，**不要太快把一个 selector 写成“组合义务唯一红”**；真正做完 `insert2/update2/basicrev` 之后，往往会长出 protocol-specific 子选择器，而不是一个对所有协议都统一的口号。对当前 severe 目标来说，plain `ADD INDEX` 这条 lane 因为已经命中主路径、no-pause、strong `MDL ON` 对照，而且还能继续最小化事务内部结构，现在已经明显比之前 richer sibling 更接近 issue-worthy。**

**2026-07-11zs 这轮把 `delayForAsyncCommit` family 真正推进到了更“像产品 bug”的主路径：plain `ADD INDEX` 自身，在没有 prewrite failpoint 放大的情况下，就已经能稳定长出自然红格；而且最小化之后发现红点并不是单个 `INSERT` 或单个 `UPDATE` 就能点亮，而是** `insert + update` **这种两步事务组合义务本身。** 这轮先没有再去扩更花的 sibling，而是把 probe `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go` 继续往**主路径最小化**推进：1) 把 `fanout3/mixed3` 支持补到 `add-index/add-unique-index/add-primary-key` 这些 single-index DDL 上；2) 再新增 `txn-shape=insert1|update1`，专门用来拆 `basic(insert + update)`。最强的新结果出现在 plain `ADD INDEX`。先看 `same-start + hold=0ms + no-pause + MDL OFF` 的 `basic` shape：`/Users/bba/pc/ai-native-assets/logs/add-index-1pc-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 当场 **RED**，随后两次复跑 `...rerun1...` 与 `...rerun2...` 也都稳定 **RED**；`MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-basic-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 是 **GREEN**。async commit 轴上，这条 main path 也已经坐实：`/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`...rerun1...`、`...rerun2...` 全部稳定 **RED**，而 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-basic-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 是 **GREEN**。这已经比前一轮 `multi-add/composite/generated` 那些 richer sibling 更强，因为现在命中的是**plain `ADD INDEX` 主路径**，而且不需要 pause-prewrite 放大镜。然后这轮又把 repro 往更小压了一步，结果非常关键：**单操作单独都不够，组合义务才会红。** `/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-insert1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-insert1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 都是 **GREEN**；`/Users/bba/pc/ai-native-assets/logs/add-index-async-commit-update1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-update1-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 也都是 **GREEN**；但只要回到两步 `basic(insert + update)`，两个协议立刻稳定 **RED**。这说明当前最好的 selector 已经不是“plain add-index 总会坏”，也不是“任何单个 DML 在 DDL 窗口里都危险”，而更像：**`MDL OFF + same-start + no-pause + plain ADD INDEX` 下，事务需要同时处理‘新增一条新索引 key’和‘改写一条旧索引 key’时，schema-change/reorg boundary 的自然 availability red 就会出现；单独 insert 或单独 update 还不够。** 这一条比之前所有需要 richer DDL 或额外 hold 的红格都更接近 issue-worthy，因为它已经是 plain `ADD INDEX` + 两步普通事务的主路径用户层故障。顺带一提，这轮 single-index sibling 也给了一个次级分叉：`add-unique-index + 1PC + basic + no-pause` 当前是 **GREEN**，但 `fanout3` 会翻成 **RED**；`add-primary-key + async/1pc + fanout3` 则都是 **RED**。这些格子先作为次级 selector 资产留着，不必比 plain `ADD INDEX` 的主结论说得更重。**

**2026-07-11zr 这轮把 `delayForAsyncCommit` family 从“需要 pause-prewrite 放大”的 near-boundary 试验，推进成了一个质量明显更高的 natural-red family：同样是 `same-start + hold=0ms`，但把 probe 新增的 `-pause-prewrite=false` 打开后，已经能在没有 failpoint 放慢 prewrite 的情况下稳定命中用户可见红格，而且 selector 比之前更清楚。** 这轮先给 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go` 新增了两个很小但很关键的能力：1) `-txn-shape=mixed3`，专门表示 `delete + insert + update` 这条介于 `fanout3` 和 `mixed4` 之间的 transaction-side semantic pressure；2) `-pause-prewrite=false`，允许把 `beforePrewrite` 放大镜彻底拿掉，直接验证有没有**自然红格**。结果非常值钱。先看 `add-virtual-generated-index-rich` 这条 lane：`async-commit + basic + no-pause + hold=0ms + MDL OFF` 的 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-async-commit-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 已经直接 **RED**，`fanout3` 的 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 也是 **RED**，新加的 `mixed3` 更是连续多次稳定 **RED**：`...nopause-mdloff-rerun1...`、`...rerun2...`、`...rerun3...` 全部报 `Information schema is changed`，而 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-async-commit-mixed3-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 则是 **GREEN**。在同一 lane 的 `1PC` 轴上，`basic` 仍 GREEN(`/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-1pc-basic-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`)，但 `fanout3`、`mixed3`、`mixed4` 都已经在 `MDL OFF` 下直接 RED；其中 `mixed3` 的 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-1pc-mixed3-cross-hold0ms-gap0ms-mdlon-20260711.log` 是 GREEN。更重要的是，这条“自然红格”并不局限在 generated family 里。沿最干净的 `fanout3 + no-pause + hold=0ms` 继续横向压 DDL kind 后，`multi-add-index-rich` 和 `add-composite-index-rich` 都在 **两个协议** 上直接红：`/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-1pc-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-1pc-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 全部稳定 **RED**；对应 `MDL ON` 对照 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-1pc-fanout3-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-1pc-fanout3-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log`、`/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdlon-20260711.log` 又全部 **GREEN**。反过来，`stored generated` sibling 在同样格子里仍然 GREEN：`/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-1pc-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 与 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-commit-fanout3-cross-hold0ms-gap0ms-nopause-mdloff-20260711.log` 都是 **GREEN**。把这些格子放在一起，当前这条 family 的 selector 已经明显升级：**不是“复杂 DDL 都更危险”，而更像“`MDL OFF + same-start + no-pause + richer amend path` 时，ordinary-column index amend(`multi-add` / `composite`) 比 stored-generated sibling 更早进入自然 availability red；virtual-generated lane 也能在更弱放大条件下长出自然红格。”** 这轮还有一个很重要的方法论坑：为了理解 `fanout3 + ddl-lead` 的非单调结果，probe 一度同步查询 `information_schema.DDL_JOBS` 做相位日志；结果这种同步观测本身就足以把 10ms/20ms/50ms 一带的微窗口推绿。于是现在 `observe-ddl-job` 已改成**默认关闭的诊断开关**，不要再把“带 observer 的阈值”直接当 selector 事实。总体上，这轮最值钱的不是又多了几个 timing 点，而是**我们第一次把同一条 DDL family 压到了 no-pause 自然红格，而且这些红格已经能沿 transaction shape / protocol / DDL-kind 三条轴重新组织出更高质量的 selector。**

**2026-07-11zp `delayForAsyncCommit` family 这轮又向前迈了一步，而且这次不是再拆 DDL side，而是把 transaction side 的 operation mix 单独拆成了一个更猛的隐藏维度：在当前最宽的 `add-virtual-generated-index-rich + 1PC + ddl_start_gap=0` 绿窗上，只要把事务从“insert + update + update”再推进成 probe 新增的 `mixed4(delete + insert + update + update)`，`MDL OFF` 下的 availability cliff 就不是再前移一点点，而是几乎整个近边界窗都塌掉了。** 这轮先给 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go` 新增了 `-txn-shape=mixed4`，只支持当前最有希望的 generated-rich lane：初始行变成 `(1,1,'a'),(2,2,'bb'),(3,3,'ccc')`，事务改成 `delete id=3; insert id=4; update id=1 -> (10,'zzz'); update id=2 -> (20,'yy')`，因此同一笔事务里同时要求 amend **删除旧 key、插入新 key、改写两个旧 key**；oracle 仍然保持原来的 exact rowset + `ADMIN CHECK TABLE`，没有换口径。live 结果非常有信息量：对比上一轮 `fanout3` 还只是把 `1PC + virtual generated` 从 `1.97s green / 1.98s red` 收紧到 `1.90s green / 1.95s red`，新的 `mixed4` 在 `MDL OFF` 下已经不只是“再提前一点”，而是 **`hold=1.8s` RED、`1.6s` RED、`1.2s` 仍 RED，甚至 `800ms` 也已经 RED**；对应日志分别是 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-1pc-mixed4-cross-hold1800ms-gap0ms-20260711.log`、`...hold1600ms...`、`...hold1200ms...`、`...hold800ms...`，全部稳定报 `Error 8028 (HY000): Information schema is changed`，而且 `AFTER_HOLD ddl_status=running`。同一 transaction shape 的 `MDL ON` control 则继续 GREEN：`...hold1200ms-gap0ms-mdlon-20260711.log` 是 GREEN。把这组格子和 `basic` / `fanout3` 放在一起，当前最值得保留的新 selector refinement 已经不只是“txn-side amend fanout 也会前移边界”，而是更强的：**transaction-side operation mix(delete + insert + multi-update overlap) 可能比单纯增加 amend fanout 更剧烈地压缩可恢复窗口。** 这条结果依然没有打出我们最想要的 `commit success + oracle red` 灰格，所以产品层目前仍然更像 availability family，而不是 silent corruption；但它对 severe 挖掘很有帮助，因为它说明下一步如果还留在这条 lane，最有希望继续长出更强后果的不是再换 DDL kind，而是继续从 transaction side 去加压。**

**2026-07-11zo `delayForAsyncCommit` family 这轮又补出一个很值钱的隐藏维度，而且是同一条 live family 内部的“事务侧 fanout”维度，不是再换模块：在 `add-virtual-generated-index-rich + 1PC + ddl_start_gap=0` 这条目前最宽的绿窗上，只把事务形状从原来的 `basic(insert one + update one)` 提高到 probe 新增的 `fanout3(insert one + update two)`，`MDL OFF` 下的 red/green 边界就明显前移了。** 这轮先顺着最新 top family 继续做 near-boundary factorization，而不是再换 lane。首先把 `async + virtual generated` 这条之前还没压实的格子补成了一个干净小矩阵：`/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-async-cross-hold1700ms-20260711.log` 是 GREEN，`...hold1750ms-20260711.log` 稳定 RED，而 `...hold1750ms-mdlon-20260711.log` 继续 GREEN，所以 **`async + virtual generated` 当前更像 `1.70s green / 1.75s red`**，并不像 `1PC + virtual generated` 那样宽。随后为了继续找灰格，又给 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go` 新增了一个极窄的 `-txn-shape=fanout3`，只提高同一事务里需要 amend 的行数和派生 key fanout：初始行改成 `(1,1,'a'),(2,2,'bb'),(3,3,'ccc')`，事务改成 `insert (4,4,'dddd') + update id=1 set b=10,pad='zzz' + update id=2 set b=20,pad='yy'`，oracle 仍然保持同一套 exact rowset + `ADMIN CHECK TABLE`。live 结果很干净：在同一 `add-virtual-generated-index-rich + 1PC + ddl_start_gap=0` family 下，旧 `basic` 事务此前已经压到 `1.97s green / 1.98s red`; 现在新的 `fanout3` 版本 `...hold1900ms-gap0ms-20260711.log` 仍 GREEN，但 `...hold1950ms-gap0ms-20260711-rerun.log` 已经稳定 RED。更关键的是，修掉 probe 自己一个 early-finished control deadlock 之后，两个 same-altitude `MDL ON` sibling 也都稳定 GREEN：`...hold1900ms-gap0ms-mdlon-timeout40s-rerun-20260711.log` GREEN，`...hold1950ms-gap0ms-mdlon-20260711-rerun.log` 也是 GREEN。于是这轮可以比较有把握地写成一个新的 selector refinement：**在同一 DDL kind、同一 txn protocol、同一 boundary、同一 exact-row oracle 下，事务侧 amend fanout 本身就是一个隐藏 `D_dim`; 它会把 failure window 往前推。** 方法论收获也很具体：1) 先把 `ddl-kind / txn-protocol / key-shape` 拆开之后，不要忘了继续拆 **txn-side amend pressure**；2) 灰格搜索不一定先长成 silent corruption，它也可能先长成“同 family 下更早的 availability cliff”，这同样能反推 selector；3) probe 自身也要能承受 `DDL 先 finish` 的控制格，这轮专门修了一个 harness bug——旧 probe 在 `ddlErrCh` 上会早读一次、后面又无条件再读一次，导致 `MDL ON` control 假死锁；现在已经改成“早 finish 只消费一次 channel”，后续这类 control 可以重新信任。当前仍然**没有**打出 `commit success + exact-row oracle red` 的 silent corruption 格，所以产品层最稳的结论还是 availability family；但现在这个 family 的隐藏维度已经从“DDL kind / 协议 / generated-vs-composite”继续扩到了 **transaction-side amend fanout**。**

**2026-07-11zk 新的 non-partition DDL availability candidate 现在已经从“`ADD INDEX` 单点异常”长成了一条更硬的 shared family，而且这条 family 不只跨 DDL kind、也不只跨 txn protocol，连更复杂的 amend path 也已经开始稳定命中：在 `MDL OFF` 路径下，`delayForAsyncCommit()` 所在的 schema-change/reorg boundary 对“slow prewrite 跨边界”这类时序似乎没有像源码/现有测试暗示的那样兜住，当前 live owner lane 上无论是 async commit 还是 1PC，只要 DDL 进入这个 shared boundary，事务都可能稳定报 `Information schema is changed`。** 这轮刻意没有再往 partition 方向漂，而是顺着 `pkg/ddl/ddl.go:1300` 那条 `delayForAsyncCommit` 义务往前做：源码明写它会在可能破坏一致性的 DDL 完成前留出 `SafeWindow + AllowedClockDrift`，目的是“make async commit and 1PC safe”；仓库里也正好有两个被 skip 的 realtikv 用例 `tests/realtikvtest/pessimistictest/pessimistic_test.go:2150` `TestAsyncCommitWithSchemaChange` 和 `:2223` `Test1PCWithSchemaChange`，都明确期待 `ADD INDEX` 期间事务成功、并且新索引 key 被 amend。基于这个证明义务，这轮先发现旧的 `add_index_async_commit_frontier_probe.go` 虽然 live GREEN，但它主要打的是 backfill frontier，不是更关键的 schema-change boundary，于是换成了更贴近义务的小 probe `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`：它直接在 failpoint owner 上打开 `tikvclient/beforePrewrite=1*pause`，让一个 `pessimistic` 事务在 `insert (2,2)` + `update id=1 set b=10` 之后卡在 prewrite；一秒后提交目标 DDL，保持 pause 一段时间，再释放 failpoint，最后用 `ADMIN CHECK TABLE` 和 index/table 双路 oracle 收尾。probe 现在已经支持两条 sibling 轴：`ddl-kind=add-index|add-unique-index|add-primary-key|multi-add-index|add-generated-index`，以及 `txn-kind=async-commit|1pc`。结果已经形成一个明显更强的矩阵。**ADD INDEX / async commit RED**：`metadata_lock=OFF` 下，日志 `/Users/bba/pc/ai-native-assets/logs/add-index-async-cross-probe-red2-20260711.log` 稳定记录 `AFTER_HOLD ddl_status=running`，随后事务直接报 `Error 8028 (HY000): Information schema is changed ... [try again later]`；**ADD INDEX / async commit GREEN sibling**：完全同配置但 `metadata_lock=ON`，日志 `/Users/bba/pc/ai-native-assets/logs/add-index-async-cross-probe-green-20260711.log` 稳定 `TXN_RESULT success`，终态行集 `1:10,2:2`，`ADMIN CHECK`、index/table 计数都绿。**ADD UNIQUE INDEX / async commit RED**：`metadata_lock=OFF` 下，`/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-cross-probe-red-20260711.log` 也稳定 RED，同样是 `AFTER_HOLD ddl_status=running` 后事务报 `Information schema is changed`；**ADD UNIQUE INDEX / async commit GREEN sibling**：`metadata_lock=ON` 下，`/Users/bba/pc/ai-native-assets/logs/add-unique-index-async-cross-probe-green-20260711.log` 稳定 GREEN。**ADD PRIMARY KEY / async commit RED-GREEN sibling** 也已经跑通：`/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-cross-probe-red-20260711.log` 在 `metadata_lock=OFF` 时稳定 RED，`/Users/bba/pc/ai-native-assets/logs/add-primary-key-async-cross-probe-green-20260711.log` 在 `metadata_lock=ON` 时稳定 GREEN。更关键的是，**1PC 轴也中了**：沿被 skip 的 `Test1PCWithSchemaChange` 直接打 `ADD INDEX`，`metadata_lock=OFF` 下 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-cross-probe-red-20260711.log` 稳定 RED，事务同样报 `Information schema is changed`；而 `metadata_lock=ON` 下 `/Users/bba/pc/ai-native-assets/logs/add-index-1pc-cross-probe-green-20260711.log` 稳定 GREEN。然后这轮又把 family 往**更复杂 amend path** 推了两格，而且把窗口明显压窄了。第一格是 **`multi-add-index`**：`metadata_lock=OFF` 下，`hold=1s` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-async-cross-hold1-20260711.log` 仍然 GREEN，而且两个索引 `idx_b/idx_pad` 的强 oracle 都绿；但继续压小矩阵后，`hold=2s` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-async-cross-hold2s-20260711.log` 已经稳定 RED，事务在 DDL 仍 `running` 时直接报 `Information schema is changed`，而更宽的 `hold=2.4s` `/Users/bba/pc/ai-native-assets/logs/multi-add-index-async-cross-hold2400ms-20260711.log` 也是 RED；同配置下 `metadata_lock=ON` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-async-cross-hold2400ms-mdlon-20260711.log` 仍稳定 GREEN。更关键的是，这条 richer amend path 已经和 **1PC** 轴发生交叉：`metadata_lock=OFF` 下 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-1pc-cross-hold2s-red-20260711.log` 也稳定 RED，而 `metadata_lock=ON` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-1pc-cross-hold2s-green-20260711.log` 稳定 GREEN。第二格是 **generated column index**：把 DDL 改成 `add-generated-index`，表结构改成 `g int as (b + 1) stored` 以后，`metadata_lock=OFF` 下 `hold=1s` 的 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-async-cross-hold1-20260711.log` 仍 GREEN，说明至少在明显安全窗内，base-column update + generated-index amend 仍能维持一致；但继续压矩阵后，`hold=2s` 的 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-async-cross-hold2s-20260711.log` 已经 RED，`hold=2.4s` 的 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-async-cross-hold2400ms-20260711.log` 同样稳定 RED，而 `metadata_lock=ON` 的 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-async-cross-hold2400ms-mdlon-20260711.log` 继续 GREEN。再加两条已经固化的控制格：`MDL OFF + 不 pause prewrite + 并发 ADD INDEX` 的对照在 `/Users/bba/pc/ai-native-assets/logs/add-index-async-cross-control-nopause-20260711.log` 是绿的；`MDL OFF + pause prewrite + 没有并发 DDL` 的对照在 `/Users/bba/pc/ai-native-assets/logs/add-index-async-cross-control-noddl-20260711.log` 也是绿的，说明 pause 本身不会把事务打死。把这些格子放在一起，当前最像真的 selector 已经不再是“`ADD INDEX` finish window”这种过窄说法，而更像 **`MDL OFF + paused/slow prewrite + shared schema-change/reorg boundary`**，并且它已经沿三条轴扩开：一条是 **不同 DDL kind**（`ADD INDEX` / `ADD UNIQUE INDEX` / `ADD PRIMARY KEY`），一条是 **不同 txn protocol**（async commit / 1PC），还有一条是 **更复杂的 amend path**（multi-schema add-index / generated-column index）。更关键的是，这第三条轴给出了一个新的 severity 线索：**single `ADD INDEX` 之前观察到的是 `1s` 绿、`3s` 红，而 richer amend path 现在已经收紧成 `1s` 绿、`2s` 红。** 这说明当前最危险的不是简单把更多 sibling 列出来，而是承认 family 在复杂 DDL 上会更早进入 failure window。方法论上这轮也很值钱：1) 旧 probe 如果只打 backfill frontier，可能会错过真正的 boundary contract；2) 对这类“源码说应该 safe”的 lane，最该打的不是 generic workload，而是一条**跨 boundary 的阻塞事务**；3) sibling 最有价值的不是换完全不同模块，而是先沿共享义务压 `same family / different DDL kind`，再压 `same family / different txn protocol`，最后压 `same family / richer amend path`，这样才能快速判断这是不是“真 root cause 在扩”，还是只是一条孤立边缘路径。当前尚未在这些 richer sibling 上打出“事务成功但强 oracle 失败”的 silent corruption 格，说明这轮最稳的结论仍是 availability root，而不是数据不一致；但 `multi-add-index` 和 generated-index 都能在 `MDL OFF` 下被同一 boundary **更早** 打红，而且 `multi-add-index + 1PC` 也已经进入同一红格，本身已经把这条 severe family 的 blast radius 和危险窗口都拉大了。下一步最值得做的是：把这条 live red 做成一个本地 realtikv 同构 sibling，最好直接沿 `TestAsyncCommitWithSchemaChange` 和 `Test1PCWithSchemaChange` 各改出 `MDL OFF + beforePrewrite pause` 变体；如果要继续往 severity 更高的方向挖，就优先在当前 probe 上继续扫“richer amend path + near-boundary green zone”，专门找 `commit success but oracle red` 的灰格。**

**2026-07-11zn issue62531 severe lane 这轮第一次把“命中 severe signature 之后,表的后态到底有没有真的坏掉”单独做成了一个 aftermath oracle,结果很关键: current live red 目前更像** `DDL 窗口中的强瞬时执行错误` **而不是** `DDL 结束后留下持久坏表态`。具体做法是给 `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 新增了 `-after-red-oracle` 开关：一旦 worker 命中 `table_scan_executor.rs:467` / `missing data for NOT NULL column (offset = 2)` 这条 severe signature,probe 不再立刻退出,而是先释放 `beforeUpdateColumnBackfillApply` failpoint,等 `ALTER TABLE ... MODIFY COLUMN` 正常收尾,把并发 worker 收住,最后继续跑原本的 final oracle(`ADMIN CHECK TABLE` + `val0/val1` 公式一致性 + table-scan/index-scan full-row reader oracle)。用当前 sharp red 格 `with-index=true, workers=16, rows_per_op=200, seed_base=1, hold=15s, worker-mode=combined, delete-session=pool` 重跑后的日志在 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-combined-pool-worker16-aftermath-20260711.log`：先稳定记录 `SEVERE signal observed ... worker 6 delete path hit issue62531 signature ... missing data for NOT NULL column (offset = 2)`，但随后 **final oracle 全绿**，日志尾部直接是 `GREEN ... final_rows=772`，最后 probe 以 `severe signature observed but final oracle stayed green afterwards` 收束。这个分叉非常值钱，因为它把 issue62531 当前的严重度画像再修正了一次：**它当然仍是 severe lane，因为在 DDL apply-window 里能稳定打出“Data is corrupted”级别的用户可见执行错误；但就现有证据看，它还不能被描述成“DDL 会留下持久数据损坏/索引损坏”，更像是窗口内某条 scan/delete consumer 在特定 row-lifecycle overlap 下读到了错误材料。** 这也解释了为什么此前 `UPDATE` / `INSERT` / DDL 结束后的后验 oracle 经常是绿的。方法论上，这轮新增了一条很实用的规则：**对 execution-path severe lane，不要只在命中 red 时停住；还要加一个 post-red aftermath oracle，区分“瞬时执行错误”与“持久坏后态”。** 这一步对 severity 判断非常关键,也能显著减少把 transient consumer failure 误写成 persistent corruption 的风险。**

**2026-07-11zm 这轮把同一条 `delayForAsyncCommit` family 继续往里拆,而不是往别的模块漂: probe 又新增了 `add-composite-index-rich` 和 `add-virtual-generated-index-rich`,目的是把 DDL amend 义务拆成更小的 `D_dims`——到底是"两条新索引的 fanout"更危险,还是"单条 composite key 编码"更危险,还是"stored generated 的派生物化"更危险,还是"virtual generated 的派生引用"更危险。** 新的 live probe 仍在 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`。其中 `add-composite-index-rich` 复用 `insert (2,2,'bb')` + `update id=1 set b=10,pad='zzz'` 这组事务,但 DDL 改成 `add index idx_bp(b, pad)`; `add-virtual-generated-index-rich` 则把表结构改成 `g int as (b + char_length(pad)) virtual`,仍然 `add index idx_g(g)`。这两格仍复用 exact rowset oracle,所以比较的是同一事务形状、同一强 oracle 下的**实现义务差异**,不是换了测试口径。live 结果现在已经给出一个更硬的 selector refinement。第一, **async 轴上 `composite` 并不比 `multi-add` 更宽**: `/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-async-cross-hold1300ms-20260711.log` 是 GREEN,而 `/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-async-cross-hold1400ms-20260711.log` 已经 RED;和 `multi-add-index-rich` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-cross-hold1300ms-20260711.log` GREEN / `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-cross-hold1400ms-20260711.log` RED 基本重合。这说明**在 async 这条 lane 上,"同时更新两列组成一条 composite key" 已经足够早地进入红区,不需要两条索引 fanout 才会坏。** 第二, **1PC 轴把不同 amend 义务真正拉开了**: `multi-add-index-rich` 的 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-1pc-cross-hold1300ms-20260711.log` GREEN 和 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-1pc-cross-hold1400ms-20260711.log` RED 说明 dual-fanout 路径仍是 **`1.3s green / 1.4s red`**; `add-generated-index-rich` 的 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-1pc-cross-hold1300ms-20260711.log` GREEN 与 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-1pc-cross-hold1400ms-20260711.log` RED 说明 **stored generated** 也同样在 **`1.3s green / 1.4s red`** 就翻红; 但 `add-composite-index-rich` 的 `/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-1pc-cross-hold1400ms-20260711.log` 还是 GREEN,到 `/Users/bba/pc/ai-native-assets/logs/add-composite-index-rich-1pc-cross-hold1500ms-20260711.log` 才 RED,更像 **`1.4s green / 1.5s red`**; 而 `add-virtual-generated-index-rich` 则显著更宽,`/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-1pc-cross-hold1900ms-20260711.log` 仍 GREEN,到 `/Users/bba/pc/ai-native-assets/logs/add-virtual-generated-index-rich-1pc-cross-hold2000ms-20260711.log` 才 RED,当前更像 **`1.9s green / 2.0s red`**。这组分叉非常值钱,因为它第一次把"复杂 amend path"这个大篮子拆成了**不同实现义务的危险宽度谱系**: `stored generated` 远比 `virtual generated` 脆,而 `composite single-index` 又明显比 `stored generated` / `multi-index dual-fanout` 更宽。第三, **旧阈值也要复核,不能把一次 bracket 当最终 selector**: 先前 `add-generated-index-rich async` 一度记成 `1.8s green / 1.9s red`,但这轮重跑时 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-cross-hold1750ms-20260711.log` 已经 RED,说明旧 bracket 至少还不够稳,当前应把它降级成"boundary needs rerun",不要直接写死成最终阈值。这里还出现了一个很值得保留的**静态根因线索**：当前事务初始化路径 `/Users/bba/pc/tidb/pkg/sessiontxn/isolation/base.go:511-516` 只会设置 `EnableAsyncCommit / Enable1PC / InfoSchema`，`/Users/bba/pc/tidb/pkg/store/driver/txn/txn_driver.go:229-318` 也没有处理 `kv.SchemaAmender`；与此同时，repo 依赖的 current `client-go`(`github.com/tikv/client-go/v2@v2.0.8-0.20260617030124-661db4f5f4e8`) 已经只保留 `SchemaLeaseChecker`，`txn.go` / `2pc.go` 里看不到旧的 `SchemaAmender/tryAmendTxn` 分支。再和 `pkg/ddl/ddl.go:1300-1323` 里 `delayForAsyncCommit()` 仍声称“make async commit and 1PC safe”放在一起看，当前 add-index family 很像不只是 threshold bug,而是**旧 amend contract 与 current runtime path 可能已经脱钩**。这条线索目前先记为 root-cause clue,不要在没有更多动态证据前直接写成最终产品结论。方法论上,这轮最重要的新增规则是: **同一 family 一旦打红,下一步优先做 obligation factorization,不要把所有 richer sibling 混成"更复杂"这一类。** 固定事务形状和强 oracle,一次只拆一个隐藏维度(fanout count / composite key / stored-vs-virtual derived mapping / txn protocol),这样得到的阴阳性才真正能反推 selector,而不是只得到一堆"某些复杂 case 会坏"的模糊印象。仍然没有打出 `commit success + exact-row oracle red` 的 silent corruption 格,所以当前最稳的产品结论还是 availability family;但方法论层面的结论已经明显升级了。**

**2026-07-11zl 这轮在同一条 family 上又补出一个更细的“复杂义务分叉”资产：把 probe 升级成 `multi-add-index-rich` / `add-generated-index-rich` 以后，oracle 不再只看 count，而是直接对每条新索引路径做 exact rowset differential；结果虽然还没打出 silent corruption，但已经把“不同 amend 义务的危险窗口”区分开了。** 新 probe 仍在 `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`，只是额外引入了两个不破坏旧证据的 `ddl-kind`：1) `multi-add-index-rich`，事务形状改成 `insert (2,2,'bb')` + `update id=1 set b=10,pad='zzz'`，要求同一事务里同时 amend `idx_b` 和 `idx_pad` 两条新索引；2) `add-generated-index-rich`，表结构改成 `g int as (b + char_length(pad)) stored`，事务形状改成 `insert (2,2,'bb')` + `update id=1 set b=10,pad='zzz'`，要求 base-column 双变更经由 stored generated column 再映射到新索引 `idx_g`。这次 oracle 也升级了：成功格不仅继续跑 `ADMIN CHECK TABLE`，还会把 `USE INDEX(...)` 路径和 `IGNORE INDEX(...)` 路径按确定排序拉成**精确行集**对比；generated-column variant 还把 `g` 本身投影出来，避免“base 列看着对、派生值其实错了”被弱 oracle 漏掉。live 结果有两个很有方法价值的点。第一，**仍然没有 silent corruption**：`metadata_lock=OFF` 下，`multi-add-index-rich` 的 `hold=1s` 日志 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-cross-hold1-20260711.log` 是 GREEN，`idx_b` / `idx_pad` 和 table path 的 exact rowset 全对；`add-generated-index-rich` 的 `hold=1s` 日志 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-cross-hold1-20260711.log` 也是 GREEN，`idx_g` exact rowset 与 `id,b,pad,g` table path 完全一致。这说明 richer amend path 至少在明显安全窗内还能成立，后面的 RED 不是“oracle 太弱导致假阴性”。第二，**复杂义务的窗口收紧速度并不相同**：`multi-add-index-rich` 在 `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-cross-hold1500ms-20260711.log` 已经稳定 RED，而 `metadata_lock=ON` sibling `/Users/bba/pc/ai-native-assets/logs/multi-add-index-rich-async-cross-hold1500ms-mdlon-20260711.log` 仍稳定 GREEN，所以这条 richer path 现在可以压成 **`1.0s green / 1.5s red`**；而 `add-generated-index-rich` 在 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-cross-hold1800ms-20260711.log` 还是 GREEN，到 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-cross-hold1900ms-20260711.log` 才翻 RED，对照 `/Users/bba/pc/ai-native-assets/logs/add-generated-index-rich-async-cross-hold1900ms-mdlon-20260711.log` 继续 GREEN，所以这条义务更像 **`1.8s green / 1.9s red`**。把这组 richer sibling 和前面的 simple sibling 放在一起，当前这条 severe family 最值钱的新结论已经不是“复杂 DDL 一概更危险”，而是更细的：**同属 `delayForAsyncCommit` family，但 multi-index 同时 amend 与 generated-column 派生 amend 的危险窗口宽度并不一样；multi-index dual-amend 更早进入红区。** 这对后续 LOOP 很重要，因为它说明下一步不该再把“richer path”当成一个大类，而应该继续按义务拆开小矩阵。方法论上的净收获有三条：1) `ADMIN CHECK + count` 还不够，复杂 amend path 必须上 **exact rowset differential**；2) 如果 richer success 格仍 GREEN，之后的 RED 更可信地是 availability/failure-window 现象，而不是弱 oracle 假红/假绿；3) 同 family 内部也要继续找 **selector refinement**，别把所有复杂 sibling 混成一个“更难”的篮子。**

**2026-07-11zj issue62531 severe lane 这轮最大的收获不是“又打红一次”，而是把一条差点写偏的 hypothesis 纠正了，并且把 current live red 压成了更干净的最小矩阵：在 testbed `8220955` 当前 owner lane 上，`combined + with-index + seed_base=1 + rows_per_op=200 + hold=15s` 这条 deterministic family 的真正 sharp 边界更像 `workers=16` 才会稳定点燃，而不是先前短暂以为的 `14`。** 这轮先把同一基线重跑成一组同日 sibling，全部日志都已落到 `/Users/bba/pc/ai-native-assets/logs/`：`modify-column-pinned-combined-pool-worker14-verify-20260711.log` 和 `...worker14-rerun...` 都是 **GREEN**；`...worker15-verify...` 也是 **GREEN**；但 `...worker16-verify...` 在完全同配置下稳定 **RED**，命中 `worker 14 delete path hit issue62531 signature ... table_scan_executor.rs:467 ... missing data for NOT NULL column (offset = 2)`。沿时间轴再压一格，`...worker16-hold10-verify...` 是 **GREEN**，说明当前 lane 对 apply-window 停留时长也很敏感；沿窗口宽度再压一格，`...worker16-rows100-verify...` 与 `...worker16-rows150-verify...` 都是 **GREEN**，而 `rows_per_op=200` 才重新转 RED。这组结果把最小红格又收紧了一层：**当前 current lane 更像 `with-index + combined worker local cycle + workers=16 + rows_per_op=200 + hold=15s` 的交叉点，而不是“随便一个高并发 broad workload 都能撞中”。** 更关键的是，这轮还把一个很诱人的但不成立的解释打回去了：先前一度怀疑 `delete-session=pool` / connection drift 是必要条件，但同一条 16-worker 红格下，`...combined-old-worker16-verify...` 和 `...combined-fresh-worker16-verify...` 也都分别 **RED**，因此 **pool drift 不是当前 combined severe lane 的必要条件**。真正值钱的 sibling 在另一条轴上：`...pairfollow-worker16-defaultsleep-verify...` 与 `...pairsplit-worker16-defaultsleep-verify...` 在同样 `workers=16, rows_per_op=200, hold=15s` 下都 **GREEN**。这说明当前更稳的 selector 不是“delete session 漂移”，而是 **same-worker combined local cycle**；只要把 insert/delete 角色拆开，即便仍在同一 apply-window、同一 value domain、同一并发量下，红格就会塌回绿。方法论上，这轮非常值得记进 LOOP：1) **对已经记录在案的 live hypothesis 也要愿意做纠偏复核**，否则 AI 很容易把一次偶发红当成稳定阈值。2) **先压 concurrency / hold / rows_per_op 这类最小矩阵，再压 session 解释轴**，可以更快把“真正控制量”和“只是看起来像解释的东西”分开。3) 当某条 severe lane 已经有多个貌似合理的故事时，最有价值的不是再讲新故事，而是主动跑会推翻旧故事的 sibling；这轮 `old/fresh 仍红` 和 `paired-split / paired-follow 皆绿` 就是典型例子。**

**2026-07-11zi issue62531 severe lane 这轮最值钱的不是“又打了一次红”，而是把 live red 再压成了一个更小、更有方法价值的必要条件矩阵：当前 current master/`fp-tidb` 上，这条红已经不再像“任何 delete / 任何 prepared delete / 任何索引路径都能单独点燃”，而更像** `MODIFY COLUMN` apply-window 内 **并发 insert+delete 的 row-lifecycle overlap**。先用当前 live owner lane 直接重跑 `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` baseline 高压格：`with-index=true,prefill=120000,workers=24,hold=20s,post_release=40s`，稳定再次命中 `worker 9 delete path hit issue62531 signature ... table_scan_executor.rs:467 ... missing data for NOT NULL column (offset = 2)`，说明这条 severe lane 不是旧日志幻觉，current testbed 仍可 live 复现。随后把最小矩阵压到四个对照格：1) **`delete-index-hint=ignore` 仍 RED**，而且同样落在 `table_scan_executor.rs:467`，这说明当前红格已经不能再简单解释成“只要 delete 选到 secondary-index lookup 才会坏”；full/table-scan 风格 delete 也能命中。2) **`skip-insert=true`(只保留 delete) 转 GREEN**：`delete_ops=666,final_rows=0`，说明“delete old-row consumer 坏掉”本身不是充分条件；没有并发 insert/reinsert 的 row-lifecycle overlap，当前高压格下删光全表也不会点燃红。3) **`skip-delete=true + oracle-mode=delete + oracle-workers=1` 转 GREEN**：也就是只保留 worker insert，唯一 delete 由 server-side prepared DELETE 执行，结果 `oracle_delete_ops=91` 但整体仍绿。这一点很关键，因为它说明“prepared delete 自己存在”也不是充分条件；单独把 delete 迁到 oracle worker 上，并不能替代原先 combined worker 的 overlap shape。4) **`op-order=delete-insert` 仍 RED**，说明顺序并不是“先插后删”才特有；只要同一高压窗口里 insert/delete 双方都在 active overlap，红格仍能被点燃。另补了一条很值得记到 LOOP 里的观察：**`before-delete-reader=ignore` 这个显式 full-row reader precheck 会把同一 red 格扰动成 GREEN**。这不应被误读成“reader 一定安全”，而应视为一个很典型的 harness-perturbation / observer effect 资产：当 probe 自己把一条 bridge 提前走一遍时，可能会改变时序、计划缓存、行像 materialization 或 cop task 切分，于是把原本的红格掩掉。把这组结果合在一起看，issue62531 当前最值得保留的新 selector 已经不是泛泛的 “delete scan during modify-column write reorg”，而是更具体的 **scan/table-scan row decode bridge + concurrent insert/delete row-lifecycle overlap**；同时方法论上又多了一条很实用的规则：**一旦 severe lane 已经能 live 打红，下一步最划算的不是继续加更大 workload，而是先压“delete-only / insert-only+prepared-delete / op-order / forced plan / intrusive pre-reader”这类小矩阵；它们对 selector 质量的提升，往往比再多打一轮红更有价值。**

**2026-07-11zh issue61255 lane 这次又往前收了一刀，而且是 live failpoint-pinned 的收口: mixed-owner temp-index merge 不只是在本地源码/UT 上看起来已经变绿，在 testbed `8220955` 的 failpoint owner `fp-tidb` 上，用 distributed `ADD UNIQUE INDEX global + ADD INDEX local` 也打出了稳定 GREEN。** 这轮新增 live probe `/Users/bba/pc/ai-native-probes/add_index_mixed_owner_merge_live_probe.go`，直接把 `beforeBackfillMerge` 挂成 `/fail/` pause，在 `dist_task=ON + fast_reorg=ON` 的 5k-row hash-partition 表上跑 `alter table ... add unique index idx_b(b) global, add index idx_c(c)`。同时在 DDL 运行阶段持续做最小 `delete(1) -> insert(2,1,1) -> delete(2)` 循环，确认 job `2353` 在 `row_count=5000`、`schema_state=write reorganization` 时真的被卡进 merge 前窗口；随后显式 settle 掉 `id in (1,2)`，再做最后一笔 `insert (4,1,1)`，最后释放 failpoint。结果日志 `/Users/bba/pc/ai-native-assets/logs/add-index-mixed-owner-dist-merge-live-20260711.log` 稳定是 `PAUSED ... dml_ops=69` 然后 `GREEN ... dist_task=true fast_reorg=true`，终态 `ADMIN CHECK TABLE`、`where b=1` 的 table path、`use index(idx_b)` 与 `use index(idx_c)` 都只剩 `4 1 1`。这条结果和前面的源码义务完全对齐：current 实现已经不是“一个 merge range 默认一个 owner”的老形状了,而是**按 temp index 前缀切 range,再在 merge executor 里用 `findIndexInfoByDecodingKey(meta.StartKey)` 绑定具体 owner**。所以 `issue61255` 现在更像**源码证明 + local GREEN + live GREEN 都一致的 retired severe lane**,不值得再继续在这条线上枚举 sibling；下一轮应把火力重新拉回更可能长出 C3 的 lane,比如 `issue62531` 的 reader/delete bridge,或者新的 distributed retry/liveness family。

**2026-07-11zg runtime-retry-loop lane 现在又补出一条更像“真的 severe”的 DDL liveness root,而且它对 LOOP 前半段和后半段都很有帮助: 我们把 distributed `ADD INDEX` / DXF ingest 的 `SetTSBeforeImportEngine` source-native `engine-not-found` 做成了同点位的小矩阵,结果不是简单 RED,而是 baseline GREEN(`job 2313 synced`) / one-shot GREEN(`job 2322 synced`) / persistent RED(`job 2319` + `global task 270003` 一直 `running`,client ALTER 挂住) / clear-fault 后恢复 GREEN。** 关键价值有三层。第一,这条不是前面 S24 那种“foreign transient fault 被当 fatal 直接 rollback”,而是镜像方向: **source-native runtime fundamental error 被当成 retryable,系统于是把 subtask 留在 `running` 并无限 rerun**。owner log 的矛盾非常硬: import path 报 `engine ... not found in SetTSBeforeImportEngine`,`lightning/common/retry` 记成 `meet un-retryable error`,但 `task_executor` 紧接着记 `meet retryable error` 和 `subtask in running state and is idempotent`。第二,这条直接把 selector 拆出新 `S25_DXF_RUNTIME_FUNDAMENTAL_RETRY_LOOP`,也把 `O28 ddl_job_liveness` 从之前只靠 `id30038` 的 `EXECUTION-CONFIRMED` 推到了 held-out `TRUSTED`: 现在它已经吃到两个完全不同的 wedge shape,一个是 upstream nightly 的 MVI write-reorg ErrCount climb,一个是 testbed `8220955` 上的 non-MVI distributed retry loop。第三,这轮再次证明方法上最值钱的不是“多打一堆 chaos”,而是 **先从源码里找到会把 plain/runtime error 默认抬成 retryable 的证明义务,再压成 same-altitude baseline/one-shot/persistent/remove 小矩阵,用强 liveness oracle 去实锤红格**。这条已正式写入远端 `found_bug id1350002`(`severity=high,status=confirmed,root_cause_id=dist-addindex-runtime-fundamental-retry-hang`),当前远端计数更新为 `85 confirmed surfaces / 62 distinct root causes`。cluster cleanup 也已做完: `tidb_enable_dist_task` 和 `tidb_ddl_enable_fast_reorg` 已恢复 `OFF`,临时库已 drop,`/fail/` 里保留的是 test API 和新 failpoint 注册名,便于后续同 owner 继续做 held-out。

**2026-07-11zf retry-classifier lane 这轮完成了一个很关键的“负校准 + 正入库”闭环: synthetic `KV/Ingest` leader-change shape 在 common txn backfill worker 上 current master 会让 `ADD INDEX` 和 `MODIFY COLUMN` 双双 `rollback done`,所以它不能算新的 S24 命中,只能算 domain-mismatch calibration;而此前已经 live-confirmed 的 modify-column transient connection/grpc-family split 现在正式写入远端 bug 库为 `id1350001`。** 先用 `/Users/bba/pc/tidb/pkg/ddl/ai_native_reorg_grpc_probe_test.go:TestAINativeIngestRetryableErrorFamilyOutcomeProbe` 做了一次很便宜但信息量很高的复核:在 common txn backfill worker 上注入 `ErrNoLeader/ErrKVNotLeader/ErrKVRegionNotFound` 这组 synthetic shape 时,当前 master 下 `ADD INDEX` job 118 也会在 `index.go:2057` 直接 `convert job to rollback`,不是 GREEN sibling;`MODIFY COLUMN` 同样 `rollback done`。这条结果的价值不在于“又发现一个 bug”,而在于它把 S24 的 gate 又写清了一层: **fault family 之外,还必须证明 altitude/domain reachability,并且要有 same-altitude GREEN control;没有这些的 synthetic all-red 只能算 calibration。** 随后把此前已经压实的 live bridge-level split 正式收束成远端 `found_bug id1350001`(`severity=high,status=confirmed,root_cause_id=modify-column-reorg-transient-unknown-fatal`)。固定证据集是 testbed `8220955` 的 commit-matched owner lane: shared GREEN control `context_deadline_exceeded` 下 `ADD INDEX` job `1755` 与 `MODIFY COLUMN` job `1758` 都 `synced`; `driver_bad_conn` split 为 `ADD INDEX` job `1761` `synced` vs `MODIFY COLUMN` job `1764` `rollback done`; `net_conn_reset` split 为 `1767` `synced` vs `1770` `rollback done`; earlier `grpc unavailable` 也是 `1723` `synced` vs `1726` `rollback done`。这条更新的 method 价值很实在: **S24 不再只是两个 index-family hit,而是已经扩成第三个非 index-family 的高严重度 DDL availability root;同时 LOOP 里要新增一条固定 pause gate: synthetic foreign fault 如果没有 same-altitude GREEN control 或自然 reachability 证明,不要把 all-red 误记成新 bug。**

**2026-07-11ze issue62531 severe lane 又补出一条很关键的“insert 侧负边界”和一条更完整的 update repair 证据链：当前最像真的 severe family 已经不是‘任何旧 session DML 都会把 changing temp column 写坏’，而更像‘DELETE 在 sparse/internal projection 上没有 repair，而 UPDATE/INSERT 至少在新值侧会补正确。’** 这轮顺着 `2026-07-11zd` 继续加最小 probe，没有再去扩 workload。第一步是在 `/Users/bba/pc/tidb/pkg/table/tables/tables.go` 的 `rebuildUpdateRecordIndices` 里再加了一个 test-only hook `beforeRebuildUpdateRecordIndexCreate`，专门抓 update 新索引写入侧的 `newVs`。结果直接把之前一个很关键但还不够执行化的猜想坐实了：`TestModifyColumnUpdateSameColumnBySecondaryIndexInWriteReorg` 里，旧值侧对 changing temp index 的 delete 仍然是错的空键，日志是 `update-side changing index delete input handle=1 ... RecordValues:[1 1 10 ] IndexValues:[]` 与 `handle=2 ... RecordValues:[2 2 20 ] IndexValues:[]`；但**新值侧在同一个 `updateRecord` 调用里会先把 hidden changing slot 修正回来，再写正确的新 changing temp key**，日志稳定变成 `update-side changing index create input handle=1 ... RecordValues:[1 11 10 11] IndexValues:[11]` 和 `handle=2 ... RecordValues:[2 12 20 12] IndexValues:[12]`。这说明 `UPDATE` 的 GREEN 不是“完全没踩到坏桥”，而更像是“旧 key 删错了，但新 key 在更后面被修正并补写了”，因此当前最好不要再把 `UPDATE` 解释成有某个神秘的 delete-side safe path。第二步是把 insert 侧从源码推断抬成执行证据：又在 `addIndices` 上加了 `beforeAddRecordIndexCreate` hook，并新增 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 的 `TestModifyColumnInsertByOldSessionWritesChangingIndexInWriteReorg`，让**旧 session**在 `beforeUpdateColumnBackfillApply` 窗口里执行 `insert into t values (4, 4, 40)`。结果是稳定 GREEN，而且 changing temp index 的 create 输入非常干净：`insert-side changing index create input handle=4 ... IndexState:write reorganization BackfillState:backfill state running RecordValues:[4 4 40 4] IndexValues:[4]`。也就是说，**至少在当前本地执行边界里，old-session insert 并不会天然把 hidden changing slot 漏成空/default；它会在 addRecord/addIndices 阶段就把 changing temp index 的新值写对。** 再把这两条和 `2026-07-11zd` 的 delete 证据放在一起看，selector 又收紧了一层：`DELETE` 在 `IndexState=write reorganization + BackfillState=running` 下，对 changing temp index 的 sparse/internal old-row delete key 仍是 `[]`，而 `UPDATE` 新值侧与 `INSERT` 新值侧都能写出正确的 `[11]/[12]/[4]`。因此当前最稳的 severe family 不是“old session DML 普遍漏写 hidden changing column”，也不是“任何 changing temp index key 变成 empty-string 就足够点燃 bug”，而更像 **delete-only old-row consumer + sparse/internal projection + row-lifecycle overlap**：new-row creation side 已经有 repair，old-row delete side 仍然暴露着坏键材料。方法论上的收获也很明确：**当一个 severe lane 同时涉及 insert/delete/update 三类 DML 时，不能只盯红的 delete 路径；必须把 green sibling 的“新值侧是否 actually 写对”也抓出来。** 这样才能把解释从“谁没坏”推进成“谁虽然踩错了，但后来修回来了”，进而逼近真正缺 repair 的 owner。

**2026-07-11zd issue62531 severe lane 又向前跨了一步，但这次最值钱的不是“证实旧猜想”，而是“推翻了我们自己一个过早的解释”。** 这轮没有再看更远的 workload，而是在 `/Users/bba/pc/tidb/pkg/table/tables/tables.go` 加了两个极窄的 test-only hook：`beforeRemoveRowIndexDelete` 抓 `DELETE -> removeRowIndices -> idx.Delete` 真正吃到的 `vals`，`beforeRebuildUpdateRecordIndexDelete` 抓 `UPDATE -> rebuildUpdateRecordIndices -> idx.Delete` 真正吃到的 `oldVs`。然后继续复用已有两个 probe。对 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg`，现在不只是看到 delete child row 的 `data_values=[1 1 ]`，而且直接看到**删索引前的真实键材料**：`public={IndexName:val0_idx Layout:[1] RecordValues:[1 1 ] IndexValues:[1]}`，`changing={IndexName:_Idx$_val0_idx_0 Layout:[2] RecordValues:[1 1 ] IndexValues:[]}`。也就是说，**同一个 sparse delete row 上，public index 删的是对的 `"1"`，但 changing temp index 真删的是 empty-string key**；再打开 `overrideUnistoreTableScanDefaultFill=return("5=1")` 后，同一条真实 delete 的日志立刻翻成 `changing ... RecordValues:[1 1 1] IndexValues:[1]`，这就把“scan decoder 翻 payload”进一步推进成了“scan decoder 翻的就是 delete 侧真正拿去删 changing temp index 的 key material”。但真正有方法论价值的反转发生在 UPDATE sibling：原本根据源码直觉，曾经以为 update downstream 可能会在删旧索引前把 changing column 从 dependency 现算回来；新 hook 直接把这个解释打掉了。`TestModifyColumnUpdateSameColumnBySecondaryIndexInWriteReorg` 虽然最终仍是 GREEN（`1 11 10 / 2 12 20 / 3 3 30`，`USE INDEX/IGNORE INDEX` 与 `admin check table t` 都绿），但 hook 明确显示：`UpdateExec` 看到的 `old_data_values=[1 1 10 ] new_data_values=[1 11 10 ]`，而真正删 changing temp index 时吃到的也是 `handle=1 ... RecordValues:[1 1 10 ] IndexValues:[]` 与 `handle=2 ... RecordValues:[2 2 20 ] IndexValues:[]`。换句话说，**“changing temp index delete key 变成 empty-string image”这件事本身，并不足以单独解释 severe red；因为 UPDATE sibling 也喂了同样的坏 key，却最终还是绿的。** 当前 selector 因而再次收窄：最稳的说法不再是“delete-specific consumer 会坏、update-specific consumer 会修”，而是更像 **DELETE sparse index-layout consumer + row-disappearance/temp-index lifecycle** 这一更窄的 family；`wrong changing-key material` 很可能只是必要条件，而不是充分条件。方法论上的收获很重要：**当 GREEN sibling 被用来证明“我们先前的解释也不对”时，不要把它当 setback；这正是 AI native fuzz 最该积累的资产。** 具体做法就是：先把 downstream consumer 真实输入抓出来，再允许 probe 去否定自己的源码直觉。这样 LOOP 前半段不只是“找怀疑点”，后半段还能持续收缩错误解释空间。

**2026-07-11zc issue62531 severe lane 又补出一个非常值钱的 GREEN sibling，而且这次不是“绿但不知道为什么”，而是“绿并且确认它也看到了同样的坏 slot”。** 在 `2026-07-11zb` 已经证明 scan decoder 这一跳能直接翻动真实 delete payload 之后，这轮没有立刻换模块，而是顺着同一个 selector 去打一个更近的 sibling：`scan-based UPDATE during modify-column write reorg`，而且专门选了更强的形状——**在 write-reorg 窗口里按 secondary index 扫描，并直接更新同一个正在被 `MODIFY COLUMN` 的 `val0`**。新增 probe 是 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 的 `TestModifyColumnUpdateSameColumnBySecondaryIndexInWriteReorg`：表结构 `PRIMARY KEY(id) + KEY val0_idx(val0)`，初始行 `(1,1,10),(2,2,20),(3,3,30)`，在 `beforeUpdateColumnBackfillApply` 窗口由旧 session 执行 `update t set val0 = val0 + 10 where val0 in (1, 2)`，随后等待 `alter table t modify column val0 varchar(16) not null` 完成，再核对最终行集、`USE INDEX/IGNORE INDEX(val0_idx)` 与 `admin check table t`。结果是 GREEN：最终稳定为 `1 11 10 / 2 12 20 / 3 3 30`，旧值 `1/2` 在快慢路径都消失，新值 `11/12/3` 在快慢路径都能正确命中，`admin check table t` 也绿。更关键的是，这轮还给 `UpdateExec` 加了极窄 test-only hook `beforeUpdateExecUpdateRecord`，因此不只是“看见绿”，而是直接看见了 update 真正吃到的 old/new payload：`writable_col_ids=[1 2 3 4] writable_col_names=[id val0 payload _Col$_val0_0] old_data_values=[1 1 10 ] new_data_values=[1 11 10 ]`。也就是说，**这个 GREEN sibling 实际上也吃到了同样的 hidden changing temp column，而且 old/new payload 的最后一个 slot 同样是 empty-string image；只是 UpdateExec/updateRecord downstream 并没有因此炸掉。** 这条边界非常关键，因为它把当前 selector 再往前收窄了一大步：问题不再像“任何 scan/materialization path 只要把 changing slot 物化成 `""` 都危险”，而更像是 **delete-specific downstream consumer + changing temp slot default-image** 这一更窄的 family。方法论上的收获也很明确：**GREEN sibling 最有价值的时候，不是简单证明“另一个操作没坏”，而是证明“另一个操作也踩到了同一条可疑 bridge，却没有坏”，这样就能把 root-cause 从“bridge 本身”继续收窄到“bridge 之后的特定 consumer/semantic use-site”。** 对 AI native fuzz 来说，这种“同桥不同 consumer”的辨别式 probe 非常值得保留，因为它比继续枚举更多 generic siblings 更能提高 severe lane 的分辨率。

**2026-07-11zb issue62531 severe lane 又把“scan decoder 这跳是不是只要改对填充值，actual delete 就会跟着翻值”做成了真实动作层的 counterfactual proof。** 在 `2026-07-11za` 把 unistore table-scan `DefaultVal` 实锤出来之后，这轮没有再换模块，而是在 `/Users/bba/pc/tidb/pkg/store/mockstore/unistore/cophandler/cop_handler.go` 新加了一个极窄的 test-only failpoint `overrideUnistoreTableScanDefaultFill`：格式是 `columnID=value`，仅用于把某个缺失列在 scan decoder 这一跳的补值临时改写。随后继续复用 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 里的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg`，在同一个 `beforeUpdateColumnBackfillApply` 窗口里做两次真实 delete。第一次不打 override，稳定日志仍是 `delete exec row shape ... child_column_ids=[1 2 5] ... data_values=[1 1 ]` 与 `unistore table-scan default fill ... column_id=5 ... default_value=""`；第二次只打开 `overrideUnistoreTableScanDefaultFill=return("5=1")`，其他什么都不改，再跑同一条 `DELETE FROM t WHERE val0 in (1, 2)`，关键日志直接变成 `delete exec row shape with scan default override: child_column_ids=[1 2 5] data_values=[1 1 1]`。也就是说，**只要在 scan decoder 这一跳把列 5 的缺省补值从 `""` 改成 `"1"`，真实 `DeleteExec` 在 hook 点看到的第三个 slot 就会立刻翻成 `"1"`；前面 executor replay 层的 patched decode `[1,1,""] -> [1,1,"1"]` 现在终于和真实动作层咬上了。** 配套回归 `go test -tags=intest ./pkg/ddl -run TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg -count=1 -v` 与 `go test -tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeletePlanCarriesChangingIndexLayoutInWriteReorg|OnDupUpdateMaintainsChangedUniqueIndexInWriteReorg|DeleteByPKMaintainsSecondaryIndexInWriteReorg)' -count=1` 都是 GREEN。这个结论的含金量很高，因为它把当前 local proof 从“真实 scan decoder 确实在吃 `DefaultVal`”再推进成了**bridge-local counterfactual 已经能改变真实 delete payload**。换句话说，issue62531 当前 strongest local picture 不只是“信息在 cop/table-scan bridge 丢了”，而是更具体的 **delete child row 的错误值恰好就是在 scan decoder 这跳被 materialize 出来的；只要在这跳改对，后面的 `DeleteExec` 载荷就跟着变对。** 方法论上的收获同样值得记下来：**当 replay 层和动作层一开始脱节时，不要停在“某层看起来像 root cause”；继续往下，在最靠近真实动作的 bridge 上做最小 counterfactual，直到你能改变 actual payload，而不只是改变 replay payload。** 这一步比再扩 sibling matrix 更能决定“下一步该写 issue，还是该继续找别的桥”。

**2026-07-11za issue62531 severe lane 又把“只是源码推断 cop/table-scan 层会按 DefaultVal 填 changing column”推进成了真实 scan decoder 边界的本地实锤。** 这轮没有再扩 sibling workload，而是在 `/Users/bba/pc/tidb/pkg/store/mockstore/unistore/cophandler/cop_handler.go` 的 `newRowDecoder(...).def` 上加了一个极窄的 test-only hook `beforeUnistoreTableScanDefaultFill`，专门抓 **unistore table-scan decoder 对缺失列实际用了什么 protobuf `DefaultVal`**。然后继续复用 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 里的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg`，把这条 hook 和已有 `beforeDeleteExecRemoveRow` / raw-row replay counterfactual 串起来。最新稳定日志已经形成四段连续证据：1) `delete exec row shape: ... child_column_ids=[1 2 5] ... data_values=[1 1 ]`，说明真实 delete 执行链确实拿到了 changing temp column slot，而且值是 empty-string image；2) 新增 `unistore table-scan default fill: requested_column_ids=[1 2 5] column_id=5 column_index=2 default_len=2 default_value="" decode_err=""`，说明**在真实 table-scan decoder 里，这个第三个 slot 就是在缺列时按 protobuf `DefaultVal` 被补成 `""` 的**；3) `executor row decoder shape: kinds=[int64 int64 string] values=[1 1 ]`，证明 generic replay decoder 也会把同一 raw row 解成 `[1,1,""]`；4) `executor row decoder with changing-col derivation: kinds=[int64 int64 string] values=[1 1 1]`，说明只要把 missing changing column 从 dependency column 现算一遍，同一份 raw row 就能翻成正确的 `"1"`。配套回归 `go test -tags=intest ./pkg/ddl -run TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg -count=1 -v` 与 `go test -tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeletePlanCarriesChangingIndexLayoutInWriteReorg|OnDupUpdateMaintainsChangedUniqueIndexInWriteReorg|DeleteByPKMaintainsSecondaryIndexInWriteReorg)' -count=1` 全部 GREEN。这个补强非常关键，因为它把 `2026-07-11y` 的“cop/table-scan layer 很像在吃 `DefaultVal`”从源码解释推进成了**真实执行边界的直接观测**：当前 strongest local picture 已经不是泛泛的 “internal delete bridge suspicious”，而是更具体的 **delete child scan 请求了 `[1,2,5]`，scan decoder 在列 5 缺失时真的只拿到了 protobuf `DefaultVal=""`，而不是任何 `ChangeStateInfo/dependency` 语义**。方法论收获也很清楚：**当 replay counterfactual 已经能翻值，但真实动作层还没被直接钉住时，最值钱的下一步不是再猜更多 root cause，而是在最近的跨层 bridge 上挂“输入/补值/输出”三联 hook，把“信息丢失发生在哪一跳”实锤出来。** 这类资产对 AI native fuzz 很重要，因为它把“像是这样”推进成“执行时就是这样”，后面无论做 live fix-validation 还是 issue 叙述都会稳很多。

**2026-07-11z issue62531 severe lane 又补了两条高价值 GREEN sibling control，把 selector 从“所有 old-image mutation path 都可能坏”收紧回“更像 scan-based delete internal projection family”。** 在 `2026-07-11y` 之后，专门沿着“changing-column old image 取值错误会不会外溢到别的写路径”做了两个最小 sibling。第一个是 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go` 新增 `TestModifyColumnOnDupUpdateMaintainsChangedUniqueIndexInWriteReorg`：表结构 `PRIMARY KEY(id) + UNIQUE KEY val0_idx(val0)`，在 `beforeUpdateColumnBackfillApply` 窗口用**DDL 前就存在的旧 session**执行 `insert into t values (1, 9, 10) on duplicate key update val0 = 3`，随后等待 `alter table t modify column val0 varchar(16) not null` 完成，再核对 `select id,val0`, `USE INDEX/IGNORE INDEX(val0_idx)` 行集，以及 `admin check table t`。结果是 GREEN：最终行集稳定为 `1 3 / 2 2`，`val0='1'` 的 fast/reference 都空，`val0='3'` 的 fast/reference 都只返回 `1`，`admin check table t` 也绿。第二个 sibling 是 `TestModifyColumnDeleteByPKMaintainsSecondaryIndexInWriteReorg`：表结构 `PRIMARY KEY(id) + KEY val0_idx(val0)`，同样在 apply-window 用旧 session 执行 `delete from t where id = 1`，DDL 完成后检查 `USE INDEX/IGNORE INDEX(val0_idx)` 和 `admin check table t`，结果也 GREEN：最终只剩 `2 2`，`val0='1'` 快慢路径都空，`val0='2'` 快慢路径都只返回 `2`。这两条 GREEN 非常关键，因为它们说明此前很诱人的 sibling 假设其实不成立：**`insert ... on duplicate key update` 的 old-row path 并没有像 delete scan 那样坏掉，`delete by primary key` 这种 point-get style delete 也没有复现出 stale secondary-index 问题。** 回头对源码一对，解释也更清楚了：`/Users/bba/pc/tidb/pkg/table/tables/tables.go:1079-1145` 的 `DecodeRawRowData` 在缺失 changing column 时本身就会走 `GetChangingColVal(...)`，所以 `getOldRow()` 读出来的 old image 并不是简单的 origin-default image；这也解释了为什么先前看起来可疑的 `/Users/bba/pc/tidb/pkg/executor/batch_checker.go:307-316` 没有点燃 ODKU sibling。方法论收获很直接：**当某个 generic-looking root-cause family 很诱人时，必须立刻配 2-3 个最像它的 sibling control 去测“是否真是整个 family 都坏”；一旦高相似 sibling 连续 GREEN，就要主动把 selector 收窄，而不是把所有用到 old image / changing column 的路径都继续 fuzz 下去。** 对 issue62531 而言，这轮之后最稳的画像更像是：`secondary-index family + delete needs internal/deletable projection + scan/coprocessor materialization bridge`，而不是“所有 modify-column 窗口里的 mutation old-image path 都危险”。

**2026-07-11y issue62531 severe lane 又把“为什么 patched replay 会翻值、patched actual delete 却不翻值”压成了一个更贴 live red 的源码结论: 真正的 delete child scan 很可能是在 coprocessor/table-scan decoder 这一层就把 changing column 当普通缺失列按 `DefaultVal` 填掉了,而这层协议里根本没有 `ChangeStateInfo` / dependency-column 元信息。** 证据链是三段咬合起来的。第一段是反事实分叉本身：`2026-07-11x` 里的 failpoint 只改 `/Users/bba/pc/tidb/pkg/executor/builder.go:5673` 的 `executor.NewRowDecoder`，对同一份 raw row replay 能把 `[1,1,""]` 翻成 `[1,1,"1"]`，但临时重跑真实 apply-window delete 时，`beforeDeleteExecRemoveRow` hook 看到的 `data_values` 仍然是 `[1 1 ]`，说明真实 delete child row 并没有在 hook 之前经过这条 executor-side replay decoder。第二段是本地真实 table scan 路径：`/Users/bba/pc/tidb/pkg/store/mockstore/unistore/cophandler/closure_exec.go:286,846` 显示 unistore table scan 用的是 `newRowDecoder(...).DecodeToChunk(...)`；而 `/Users/bba/pc/tidb/pkg/store/mockstore/unistore/cophandler/cop_handler.go:500-542` 里的 `newRowDecoder` 在列缺失时只会看 `tipb.ColumnInfo.DefaultVal`，然后 `decoder.DecodeOne(info.DefaultVal, ...)`，完全没有 `ChangeStateInfo` 分支。mockcopr 那边 `/Users/bba/pc/tidb/pkg/store/mockstore/mockcopr/cop_handler_dag.go:220-233` 也是同一模式：`rowcodec.NewByteDecoder(..., defVal)`，`defVal` 直接返回 `col.DefaultVal`。第三段是 pushdown 协议本身：`/Users/bba/pc/tidb/pkg/util/misc.go:321-360` 的 `ColumnsToProto/ColumnToProto` 给 `tipb.ColumnInfo` 只塞 `ColumnId/Tp/Collation/Flag/Elems` 等通用列属性；`/Users/bba/pc/tidb/pkg/table/tables/tables.go:1877-1897` 的 `SetPBColumnsDefaultValue` 只把 `GetColOriginDefaultValueWithoutStrictSQLMode(...)` 编成 `pbColumns[i].DefaultVal`；而 `tipb` 的 proto 定义 `/Users/bba/.gvm/pkgsets/go1.25.1/global/pkg/mod/github.com/pingcap/tipb@v0.0.0-20260127060946-1852f9829ce3/go-tipb/schema.pb.go:80-89` 也证实 `tipb.ColumnInfo` 只有 `ColumnId/Tp/.../DefaultVal/PkHandle/Array`，根本没有 `ChangeStateInfo` 或 dependency offset 之类的信息。把这三段放在一起，当前最像真的 live root-cause family 已经比 `2026-07-11x` 更靠近 severe red：**TiDB delete/internal scan 会把 changing temp column 当成普通 scan column push 到 coprocessor；pushdown protocol 只保留“缺失时填什么默认值”，不保留“这个列其实应由哪个 dependency column cast 得来”；于是 coprocessor/table-scan decoder 在缺列时只能按 `DefaultVal`/origin default 填，最终把 changing slot materialize 成 `""` 而不是 `"1"`。** 这比单纯说 “executor fallback 错了” 更有解释力，也更贴近远端报错来自 `table_scan_executor.rs` 的事实。方法论上的收获同样重要：**counterfactual patch 如果只在 replay 层翻值、真实动作层不翻，下一步就不要继续在同一层打转，而要顺着真实数据流往下游协议/执行边界看“信息是不是已经在跨层时丢了”。** 这类“协议里缺元信息导致 safe path 不可能做对”的形态，特别值得 AI native fuzz 在 LOOP 里单独设成高优先级 severe target。

**2026-07-11x issue62531 severe lane 又把 execution-bridge 从“值流可疑”推进成了“本地因果试验成立”: 同一份 raw row 走当前 `executor.NewRowDecoder` 会把 changing slot 解成 `""`,但只要在 test-only 开关下把 missing changing column 从 dependency column 现算一遍,第三个值就稳定从 `""` 变成 `"1"`。** 这轮没有再扩 live workload，而是把 `2026-07-11w` 的 mechanism clue 做成了一个更强的局部反事实实验。具体做法是：继续使用 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go:747` 的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg`，先保持现状证明不变，日志仍稳定打印 `delete exec row shape: table_id=116 data_len=3 child_column_ids=[1 2 5] ... data_values=[1 1 ] ...` 和 `executor row decoder shape: kinds=[int64 int64 string] values=[1 1 ]`，也就是当前执行器路径对 changing temp column `_col$_val0_0` 的实际解码值仍是 empty-string default。然后给 `/Users/bba/pc/tidb/pkg/executor/builder.go:5673` 的 `executor.NewRowDecoder` 加了一个极窄的 test-only failpoint `fixNewRowDecoderChangingColDefault`：当请求列本身带 `ChangeStateInfo`，且 dependency column 已经在同一 row decode 里先被解出来时，临时改用 `table.CastColumnValue(...)` 从 dependency datum 现算 changing column，而不是直接走 `table.GetColOriginDefaultValue(...)`。在同一个 UT 里打开这个 failpoint 后，再对**同一份 raw row bytes** 调一次 `executor.DecodeRowValToChunk(...)`，关键日志稳定变成 `executor row decoder with changing-col derivation: kinds=[int64 int64 string] values=[1 1 1]`；配套验证 `go test -tags=intest ./pkg/ddl -run TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg -count=1 -v` 与 `go test -tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeletePlanCarriesChangingIndexLayoutInWriteReorg)' -count=1` 都是 GREEN。这个资产的意义非常大，因为它把此前的结论从“源码路径看起来可疑”推进成了更接近 root-cause 的 **counterfactual proof**：在不改 delete layout、不改 old session/public admission、不改 live table metadata 的前提下，光是把 changing slot 的填充值策略从 `origin default` 改成 `derived cast value`，同一份 row image 就会从 `[1,1,""]` 变成 `[1,1,"1"]`。补充一个同样重要的负结果：在这个 failpoint 开着时，又临时重跑过一次**真实的 apply-window delete**，`beforeDeleteExecRemoveRow` hook 看到的 `data_values` 仍然是 `[1 1 ]`，没有跟着翻成 `"1"`。这说明当前 counterfactual 已经坐实了 **raw-row replay / generic row decode** 这一层，但 delete 真正吃到的 child row 还隔着别的桥，不能简单等同成“实际 delete path 直接调用了这个 `NewRowDecoder` 并把值带到了 hook 点”。换句话说，当前 issue62531 的最强本地机制已经不是泛泛的 “delete/internal projection 更脆”，而是更具体的 **generic NewRowDecoder missing-column fallback 确实会把 changing column 错填成 default image，而真实 delete row 在 hook 之前还存在另一层 materialization / projection bridge**。方法论上的收获也更清楚了：**当 strong oracle 已经把坏值流抓出来后，下一步最值钱的动作不是再扩 sibling matrix，而是做一个最小 counterfactual patch/failpoint experiment，验证“只改这一条推测中的错误推导规则，坏值会不会立刻翻成好值”；如果只在 replay 层翻值、真实动作层不翻，就说明还要继续往真正的 mutate boundary 贴 hook，而不是过早宣布 root-cause 已经完全闭环。** 这一步能显著提高 AI 对 root-cause 的置信度，也让后续是否值得去做 live fix-validation / issue 叙述更有底气。

**2026-07-11w issue62531 severe lane 又把 delete bridge 的第 3 个 slot 从“像是 changing column”推进成了“明确就是 changing column，而且当前本地路径下被 `NewRowDecoder` 塞成了 default empty string”。** 在 07-11v 的 hook 基础上，又把 `/Users/bba/pc/tidb/pkg/executor/delete.go` 的 test-only payload补成 `child_column_ids / child_column_names / data_kinds / data_values`，然后继续用 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go:741` 的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg` 去卡同一个 apply-window。最新稳定日志已经不是抽象的 “data_len=3 + layouts=map[1:[1] 2:[2]]”，而是更具体的：`table_id=116 data_len=3 child_column_ids=[1 2 5] child_column_names=[test.t.id test.t.val0 test.t._col$_val0_0] data_kinds=[int64 int64 string] data_values=[1 1 ] public_cols=4 writable_cols=5 deletable_cols=5 table_columns=5 deletable_indexes=2 layout_index_count=2 layout_max_offset=2 layouts=map[1:[1] 2:[2]]`。这条 shape 意义非常大，因为它把 `DeleteExec` 手里的 3 个 slot 解释清楚了：slot0 是 handle/id, slot1 是 public `val0`, slot2 则已经明确是 changing temp column `_col$_val0_0` (column id 5)，而且在这个本地 fixture 里它的实际值不是 cast 后的 `"1"`，而是 **default empty string**。这条现象再和源码锚点一对，mechanism clue 就更扎实了：`/Users/bba/pc/tidb/pkg/executor/builder.go:5673` 的 `executor.NewRowDecoder` 给 `rowcodec.NewChunkDecoder` 提供的 `defVal`，在列缺失时只会 `table.GetColOriginDefaultValue(...)`；而 `/Users/bba/pc/tidb/pkg/util/rowcodec/decoder.go:240-259` 的 `ChunkDecoder.DecodeToChunk()` 在 row 里找不到该列、又不是 handle 时，会直接走这个 `defDatum`，不会像 `/Users/bba/pc/tidb/pkg/util/rowDecoder/decoder.go:105-107` 那样对 `ChangeStateInfo` 调 `tables.GetChangingColVal(...)`。因此，当前最像真的本地 execution-bridge 不是“changing slot 已经被正确 cast 进 child row”，而是更尖锐的：**old-session delete child scan 已经请求到了 changing column(id=5)，但 chunk decode 的 generic default path 把缺失列填成了 empty-string default，而不是从 dependency column(val0) 派生 cast 值。** 这条线还不能单独等价成最终 remote severe root cause，因为本地 mock-store case 下 delete 仍然成功；但它已经把 07-11v 的 bridge 再压深一层，从“latest delete layout 已经落地”推进成“latest delete layout 里的 changing slot 目前看起来是用错了填充值策略”。方法论上的收获也很明确：**当 hook 已经证明 executor carries the suspicious slot 时，下一步最值钱的不是继续看更多 sibling matrix，而是继续把 slot 身份和值都采出来，再回源码里找“这个 slot 是由哪条 generic fallback 填出来的”。** 这一步能把“结构可疑”升级成“错误值流”的证据。

**2026-07-11v issue62531 severe lane 又补出一条比 `schema-view split` 更贴执行面的本地硬证据: 同一个老 session 在 apply-window 里实际执行的 `DeleteExec`，确实已经拿到了 `writable/deletable/table_columns=5 + changing temp index layout`，但它的 `public_cols` 仍然是 4，而且 child row 已经被 prune 成只剩 3 个 datum。** 这轮没有再扩 live workload，而是给 `/Users/bba/pc/tidb/pkg/executor/delete.go` 加了一个极窄的 test-only hook `beforeDeleteExecRemoveRow`，让本地 UT 可以在真正调用 `RemoveRecord` 之前抓到 delete 执行链手里的 row shape / table shape / index layout；配套资产还是落在 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go:742` 的 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg`。新的稳定证据链是这样的：1) 同一个老 session 的 `GetInfoSchema().TableInfoByName(...)` 仍然只看见 4 列 public schema；2) `external.GetTableByName` 抓到的 latest domain view 则已经是 `liveTbl.Cols()=4`，但 `(*tables.TableCommon).Columns=5`、`DeletableCols()=5`，并且 `DeletableIndices()` 里已经出现 changing temp index；3) 如果手工强绑 `domain.GetDomain(session).InfoSchema()` 去规划同一条 `DELETE FROM t WHERE val0 IN (1,2)`，仍然稳定得到 `The target table t of the DELETE is not updatable`；4) 但**同一个老 session** 在同一窗口里真实执行 `BEGIN; DELETE ...; ROLLBACK` 是成功的，而且 hook 明确抓到实际 `DeleteExec` shape 为 `table_id=116 data_len=3 public_cols=4 writable_cols=5 deletable_cols=5 table_columns=5 deletable_indexes=2 layout_index_count=2 layout_max_offset=2 layouts=map[1:[1] 2:[2]]`。其中 `map[1:[1] 2:[2]]` 非常关键：它说明 delete child row 已经被 prune 成只保留 handle/public-index/changing-temp-index 需要的 3 个 slot，而 changing temp index 的 layout 确实已经混进了执行路径。配套验证是 GREEN:`go test -tags=intest ./pkg/ddl -run TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg -count=1 -v` 会稳定打印上述 row shape，连同 `go test -tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeletePlanCarriesChangingIndexLayoutInWriteReorg)' -count=1` 也一起通过。这个资产的意义很大，因为它把之前 07-11u 里“schema-view split 只是 local mechanism clue”再收紧成了**真正的 execution bridge**：老 session 的 public admission 仍是 4 列，但一旦请求被放行，DeleteExec 实际拿到的 table object/row layout 已经带着 5 列 internal delete shape 和 changing temp index layout 在跑。换句话说，issue62531 最像真的不再只是“public reader vs delete/internal projection split”，而是更具体的 **old-session public admission -> pruned child row -> latest delete layout bridge**。方法论上，这轮也沉淀出一条很值得复用的 LOOP 补丁：**当 live red 已经稳定、local static/source reading 也给了一个很强的 mechanism clue 时，不要只停在 planner/metadata 层；直接在真正的 mutate/read boundary 上挂一个一跳 hook，把 `planner sees what`、`executor carries what`、`row payload actually contains what` 三件事同时采出来。** 这比单纯继续扩 sibling workload 更接近 root cause，也更容易区分“看起来像”与“执行时真是这样”。

**2026-07-11u issue62531 severe lane 把 `session-age / session-view` 从本地 hypothesis 抬到了 live sibling matrix，结论是“schema-view split 很像真的 internal bridge，但 old session 不是 live severe red 的必要触发条件”。** 先对上一轮本地证据做一个很重要的纠偏：`/Users/bba/pc/tidb/pkg/testkit/external/util.go:31` 的 `external.GetTableByName` 会先 `domain.GetDomain(tk.Session()).Reload()`，再从 `dom.InfoSchema()` 读表，所以 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg` 里抓到的 5 列 live table object 代表的是 **latest domain view**，不是“同一个老 session 自己突然看到了 5 列”。因此这条 UT 更准确的含义是：在 `beforeUpdateColumnBackfillApply` 窗口，latest domain/internal delete shape 已经进入 `changing column + changing temp index + DeletableCols()=5` 的状态，而同一个老 session 的 `GetInfoSchema()` 仍停在 4 列 public view；更进一步，如果手工强绑 `domain.GetDomain(session).InfoSchema()` 去规划同一条 `DELETE` 会报 `not updatable`，但老 session 自己实际执行 `BEGIN; DELETE ...; ROLLBACK` 仍然成功。接着把这个机制猜想抬到 live harness `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go`，新增 `delete-session=pool|old|fresh` 和 `delete-start=immediate|after-pause` 两个最小旋钮，用完全相同的 severe config `with-index=true,worker_mode=paired-follow,dml_sleep=50ms,prefill=120000,prefill_base=10000,worker_base=0,seed_base=1,rows_per_op=200,hold=20s,post_release=20s` 跑 sibling。结果是：`delete_session=old,delete_start=after-pause` 在日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-deleteold-afterpause-20260711.log` 继续 RED，命中 `worker 13 delete path hit issue62531 signature on [700..899] ... missing data for NOT NULL column (offset = 2)`；`delete_session=fresh,delete_start=after-pause` 在 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-deletefresh-afterpause-20260711.log` 也 RED，命中 `worker 1 delete path hit issue62531 signature on [30..229] ... missing data for NOT NULL column (offset = 2)`；但 sibling `with-index=false,delete_session=fresh,delete_start=after-pause` 在 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-deletefresh-afterpause-noindex-20260711.log` 是 GREEN。这个结果非常值钱，因为它把“session-age 这条轴”从一个很诱人的猜想压回了更精确的位置：**old session / stale session-view 不是当前 live severe red 的必要触发条件；secondary-index family 仍然是必要背景，而且 delete 即使在 pause 后用 fresh session 才开始，也能踩中同一类坏 row-image。** 因而现阶段最稳的 selector 仍然是 `secondary-index family + fast paired-follow delete + apply-window overlap`；`schema-view split + delete bridge` 则是当前最强的 local mechanism clue，但还不能直接当 externally necessary trigger。方法论收获也更扎实了：**本地机制证明只能先当 hypothesis，必须再用 live sibling cells 去测试它究竟是必要条件、充分条件，还是只是解释力很强的内部桥。**

**2026-07-11t issue62531 severe lane 又补出一条很强的“schema-view split”本地证据: latest internal table state 已经带 changing column + changing temp index，但同一个老 session 仍然只看见 4 列 public schema，并且能在 `beforeUpdateColumnBackfillApply` 窗口里成功执行 `DELETE`; 同时如果强行拿 latest InfoSchema 去规划同一条 DELETE，则会直接得到 `The target table t of the DELETE is not updatable`。** 这一轮没有再扩大 live 矩阵，而是把上一轮 `2026-07-11s` 里提到的 “public projection vs delete/internal projection split” 压成了一个本地可执行证据，落在新增 UT `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go:740`。测试 `TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg` 做了三件互相咬合的小事：1) 在 `beforeUpdateColumnBackfillApply` 窗口直接抓 live table object，确认 `liveTbl.Cols()` 仍是 4 个 public 列，但 `(*tables.TableCommon).Columns` / `liveTbl.DeletableCols()` 已经是 5 列，并且 `DeletableIndices()` 里确实出现了引用 changing column 的 temp index；2) 用同一个老 session 的 `GetInfoSchema().TableInfoByName(...)` 再看一遍，得到的仍然是 4 列 public view；3) 对同一条 `DELETE FROM t WHERE val0 IN (1,2)`，如果手工调用 `planner.Optimize(..., domain.GetDomain(session).InfoSchema())` 强行绑定 latest InfoSchema，规划会稳定返回 `[planner:1288] The target table t of the DELETE is not updatable`，但**同一个老 session** 在同一窗口里实际执行 `BEGIN; DELETE ...; ROLLBACK` 却是成功的。配套验证已通过：`go test -tags=intest ./pkg/ddl -run TestModifyColumnDeletePlanCarriesChangingIndexLayoutInWriteReorg -count=1` 以及和前一个 apply-window control 一起跑的 `go test -tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeletePlanCarriesChangingIndexLayoutInWriteReorg)' -count=1` 都是 GREEN。这个资产的价值很高，因为它把 severe family 再向前收紧了一层：**远端红格很可能不只是“delete 请求了更脆的 internal projection”，还叠加了“session admission/plan view 仍是旧 public schema，而执行桥已经落到了新 internal delete path”这种 schema-view split。** 这也解释了为什么上一轮里 “删前 public full-row reader 能成功，delete 仍能炸” 并不矛盾: public reader 和 delete 根本不一定站在同一个 schema-view / internal projection 组合上。方法论上，这一轮沉淀出的下一步很明确：把 future live probe 从“值域/时序/索引 hint”三维，再升一维到 **session-age / session-view**。也就是显式区分“DDL 前就建立并保活的老 session”“DDL pause 后新建的 fresh session”“同一逻辑 SQL 但强行 latest-infoSchema 规划”的 sibling cells。只要这条轴在 live 红格上继续成立，issue62531 的 selector 就会从 today 的 `fresh-row delete timing + secondary-index family` 再升级成更强的 `schema-view split + delete bridge` family。

**2026-07-11s issue62531 severe lane 又从“fresh rows 足够”推进到“delete-internal projection 比 public projection 更脆”: disjoint-domain red/noindex-green 坐实 fresh rows family，而同窗口 pre-delete public full-row reader 成功后 delete 仍可 RED，说明当前最像真的 bug 不是‘这批行在 public 视角下先天已经不可读’，而是 delete 语义链路请求到的那组 deletable/internal 列更容易把坏态暴露出来。** 这轮先把 07-11r 前半句里尚未正式写入 handoff 的 strongest evidence 补齐：在 `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 新增 `prefill-value-base / worker-value-base` 后，直接把 prefill 域和 live worker 域切开。配置 `with-index=true,worker_mode=paired-follow,dml_sleep=50ms,prefill=120000,prefill_base=10000,worker_base=0,seed_base=1,rows_per_op=200,hold=20s,post_release=40s` 在日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-disjoint-domains-red-20260711.log` 上稳定 RED，命中 `delete path hit issue62531 signature ... missing data for NOT NULL column`; sibling `with-index=false` 在 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-disjoint-noindex-green-20260711.log` 是 GREEN。由于 prefill 行只活在 `10000..10999` 而 live worker/delete 窗口只活在 `0..999`，这已经把 family 从“旧行也许参与了坏态”收紧成 **freshly inserted/live-domain rows 本身就足够点燃 severe red，但 secondary-index family 仍然是必要背景**。在这个基础上，又补了一个更有解释力的小旋钮 `before-delete-reader=none|use|ignore`，让 delete worker 在真正执行 delete 之前，先对同一组 `val0` 做一次 public full-row reader(`select id,val0,val1,padding ...`)。最关键的红样本是 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-predelreader-use-attempt2-20260711.log`：配置 `worker_mode=paired-follow,with-index=true,before_delete_reader=use,prefill_base=10000,worker_base=0,dml_sleep=50ms`，先记录同窗口 pre-read 成功读到 `pre_delete_rows=354`，随后同一 worker 仍在 **完全同一个 source/delete 窗口** 上命中 `worker 15 delete path hit issue62531 signature on [186..385] (source [186..385], pre_delete_rows=354) ... missing data for NOT NULL column`。这里要非常精确地解读：pre-read 成功只证明 **public projection** 还可读；它并不等价于 “delete 需要的 internal/deletable projection 也可读”。这反而和源码线索对上了：`pkg/planner/core/logical_plan_builder.go` 在 delete 分支显式选 `tbl.DeletableCols()`，而不是普通 select 那组 public `tbl.Cols()`，所以当前最像真的 gap 是 **delete child scan 请求到了比 public select 更敏感的一组列/布局**。再往前压一格：强制 `delete_index_hint=ignore` 但保留 `before_delete_reader=use` 时，日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-predelreader-use-deleteignore-attempt1-20260711.log` 仍然 RED，且同样带 `pre_delete_rows=175`。这说明 **即使最后执行的是 forced table-scan delete，只要删前 public reader 读同窗口成功，delete 仍可在 table scan executor 上炸掉**。相反，`delete_hint=ignore + before_delete_reader=ignore` 连续两轮日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-predelreader-ignore-attempt1-20260711.log`、`...attempt2...` 都 GREEN；这条边界要非常诚实地解读为 **table-scan pre-read 自己会明显扰动时序，甚至可能“降火”**，而不是“forced table scan delete 其实没问题”。再配一个负校准：`insert-only + prepared select oracle` 的 disjoint-domain run `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-insertonly-select-disjoint-attempt2-20260711.log` 是 GREEN，说明 **泛泛的并发 public full-row reader 还不足以单独点燃红格**；真正高收益的是“贴着 delete 窗口去读，再立刻删”。方法论上，这一轮又把 LOOP 补得更像样了：1) 当 broad red 已经稳定存在，下一步别急着扩 workload，先在危险动作前加一个最小 pre-action oracle，看 bug 是“public 前置状态已坏”还是“动作请求到的 internal projection 更脆”。2) `fresh-vs-old` 这种争议不要靠口头猜，直接做 disjoint-domain。3) pre-read 既能当 oracle，也可能是强干扰项；因此每次加 observation 都要配 sibling control，不然 AI 很容易把“测量改变了系统”误判成 root-cause 消失。下一步最值得做的不是再扩矩阵，而是 **在 delete child scan / row decode 上加极窄 instrumentation，直接记录它到底额外请求了哪些 changing/internal 列，以及报错前最后一个成功 decode 的列集合**。

**2026-07-11r issue62531 severe lane 继续收紧到“fresh-row deletion timing”这一层: 默认 `paired-follow` 多次 GREEN，但把 follow 节奏压到 `50ms` 后即可 RED,说明‘别人快速删你刚插的窗口’也足以点燃 severe family。** 在 07-11q 的基础上，又把 harness `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 补成更精确的跟随机制：新增 `worker_mode=paired-follow`，让偶数 worker 先插入某个窗口，只有 insert 成功后才把窗口发布给配对的奇数 worker；奇数 worker 则删除配对 worker 最近一次成功插入的窗口，而不是像 `paired-split` 那样只是“共享 seed 但各跑各的”。这个修正很关键，因为早期版本是“先发布窗口再插”，得到的 green 不能过度解读；修正后再观察，结论清楚很多。先看默认节奏(`dml_sleep=500ms`):三轮 `paired-follow` 日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-attempt1-20260711.log`、`...attempt2...`、`...attempt3...` 全部 GREEN，分别记录 `insert_ops≈457/474/469, delete_ops≈405/399/383`。这说明 **仅仅“由另一个 worker 去追最近一次 freshly-inserted 窗口”，在默认 500ms 节奏下还不够稳定点燃 bug**。但把时序再压紧一层后，family 立刻露头：`worker_mode=paired-follow,dml_sleep=50ms` 的第一轮就 RED，日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairfollow-fast-attempt1-20260711.log` 明确命中 `worker 9 delete path hit issue62531 signature on [616..815] (insert [127..326]): Error 1105 (HY000): [components/tidb_query_executors/src/table_scan_executor.rs:467]: Data is corrupted, missing data for NOT NULL column (offset = 2)`；同一轮交互式 run 也曾命中 `delete [557..756] (insert [595..794])`。这条边界非常有价值，因为它把 07-11q 里“paired-split 双绿”那条结论再向前推进了一步: **不是必须同一个 worker 自己回头删自己刚插的行；另一条 worker 也可以点燃 bug，只要它删除 freshly-inserted family 的时机足够近。** 同时也让 root-cause family 更清晰了:当前 severe 现象的关键控制量不是简单的“有 insert + 有 delete”，而是 **delete 命中 freshly-inserted / freshly-rewritten family 的时间距离**。再结合源码阅读的新负边界，`pkg/table/tables/tables.go:addRecord` 明确会在 modify-column write reorg 期间为非 public changing column 执行 `CastColumnValue(...)`，并在 `len(r) < len(t.WritableCols())` 时把 casted changing-column datum append 进 `r` 再编码落盘；因此当前 root-cause 假设已经可以排除掉一个过于粗糙的解释: **“新插入的行天生漏写 changing column”并不是最像真的问题。** 更像的 family 是: row image 在 insert/close-follow delete/reorg overlap 之间进入某个短暂坏态，而 delete 读到它时才报 `missing data for NOT NULL column`。方法论上，这一轮又沉淀出一个很重要的 LOOP 细节:当 `paired-split` 这类“共存但不跟随”的 split-role 仍然绿时，不要急着下“同 worker 必要”的结论；先把 pair-follow 做成“成功发布后再追删”，再把 inter-op delay 压一格。这个小升级比继续扩更大随机矩阵的收益高得多，因为它直接回答了 family 的因果方向。

**2026-07-11q issue62531 severe lane 又压出一个更强的“顺序/耦合”边界: insert-only 与 delete-only 都是 GREEN，`delete-insert` 仍可 RED，而 `paired-split` 连续 GREEN，说明危险形状更像“快速 delete freshly-inserted family”而不是泛泛的‘系统里同时有人插有人删’。** 在 07-11p 之后，又继续给 live harness 加了两个最小旋钮：1) `-op-order=insert-delete|delete-insert`，控制单个 worker 内的动作顺序；2) `-worker-mode=combined|paired-split`，其中 `paired-split` 让偶数 worker 只插、奇数 worker 只删，而且每对 worker 共享同一随机窗口，因此能区分“同一 worker 自己插完再删”和“系统里只是同时有人插、有人删”。先看单动作边界：`insert-only`(`skip_delete=true`) 在 `with-index=true,prefill=120000,workers=16,seed_base=1,rows_per_op=200,hold=20s,post_release=20s` 上 GREEN，现场输出记录 `insert_ops=1447,delete_ops=0,final_rows=410000`;而 07-11p 已经证明 `delete-only` 在同样高压格下连续 GREEN。于是可以比较有把握地说：**insert 本身不报 severe，delete 本身也不报 severe；当前用户可见炸点是 delete 去读某类过渡态行时触发的。** 再看顺序边界：把同一 worker 顺序反过来做 `delete-insert`，在 `ai_native_issue62531_delete_insert` 上仍然 RED，直接命中 `worker 9 delete path hit issue62531 signature on [335..534] ... missing data for NOT NULL column (offset = 2)`。这说明“必须同一轮先插后删”不是必要条件；更像是 **系统里只要持续有 insert 生成新行，后面某次 delete 迟早会踩到坏态**。但最有价值的新边界来自 `paired-split`:在与原始 severe 红格几乎相同的配置下，`with-index=true,prefill=120000,workers=16,seed_base=1,rows_per_op=200,hold=20s,post_release=40s,worker_mode=paired-split` 连续两轮都 GREEN，其中一轮已固化为日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-pairedsplit-green-20260711.log`，记录 `CONFIG ... worker_mode=paired-split ...` 之后最终 `GREEN ... insert_ops=461 delete_ops=406 ... final_rows=1396`。这条绿边界非常强，因为它表明 **单纯“系统里同时有人插、有人删”还不够；原始 severe red 更像要求同一个 worker/同一时序上，delete 紧跟着自己刚刚插入过的窗口反复打转，才会把某个家族的行推到 delete 可见的坏态。** 还额外做了一个 `delete_shift=400` 的 logical-window 错位实验，结果一绿一红：第一轮 `ai_native_issue62531_shift400` GREEN，但 rerun `ai_native_issue62531_shift400_rerun` 又 RED，而且报错里明确带了 disjoint 窗口 `delete [172..371] (insert [772..971])`。这里要非常诚实：这还不能证明“逻辑键完全无关”，因为在 `combined` 模式下不同 worker 之间仍可能有跨 worker 的窗口重叠；但至少说明 **仅靠给单个 worker 的 delete/insert 窗口错位，还不足以稳定消灭红格**。综合 07-11o/p/q，当前最像真的 root-cause family 不是“有索引就坏”或“默认 IndexLookUp 坏”，而是更窄的：**modify-column write reorg 窗口里，存在 secondary-index family 时，某种快速的 insert-then-delete/close-coupled revisit 会把 delete 路径暴露给临时坏 row-image；insert-only、delete-only、以及成对分离的 insert/delete 都不足以稳定点燃。** 方法论层面的升级也很明显：1) 当 broad workload 已经能 RED 时，下一步最有效的不是再堆更大矩阵，而是给 harness 增加最小的“顺序/角色”控制旋钮，把 family 从“并发很复杂”压成“哪种耦合关系危险”。2) `paired-split` 这种 shared-seed / split-role 对照尤其适合在线 DDL 并发 bug，因为它能把“同一 actor 的局部时序”与“全局共存”分开。3) `delete_shift` 这类看似很强的负边界，如果没有控制 cross-worker overlap，就不能过度解读；AI 在这里要主动写出“结论成立到哪一层，不成立到哪一层”，否则很容易把 mixed 结果误当 definitive green。

**2026-07-11p issue62531 severe lane 又压出两条很关键的 controlling-variable 边界: `IGNORE INDEX` 仍能 RED,但 `delete-only` 连续 GREEN,所以真正危险形状更像“secondary-index family + insert/delete 往返 + apply-window overlap”,而不是单纯默认 `IndexLookUp` 或单纯 delete 压力。** 在把 07-11o 的“窗口内瞬时 SQL 失败”结论写清后，这轮继续问两个更细的问题。第一，当前 severe red 是否只是因为默认 delete 会走 `IndexLookUp -> TableRowIDScan`？为此直接把 live harness `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 扩成可控 access-path 版本，新增 `-delete-index-hint=auto|use|ignore`。然后在最接近历史红格的配置上做 live 对照:`with-index=true,prefill=120000,workers=16,max_val0=1000,seed_base=1,rows_per_op=200,hold=20s,post_release=40s`。结果非常关键:强制 `IGNORE INDEX(val0_idx)` 后仍然能 RED，日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-deletehint-ignore-attempt1-20260711.log` 明确记录 `CONFIG ... delete_hint=ignore ...` 之后直接命中 `worker 5 delete path hit issue62531 signature on [861..60]: Error 1105 (HY000): [components/tidb_query_executors/src/table_scan_executor.rs:467]: Data is corrupted, missing data for NOT NULL column (offset = 2)`。这说明 **当前 bug 并不依赖默认 public-index `IndexLookUp`；即使强制走 `TableFullScan` 删除路径，只要 secondary-index / temp-index family 还在，窗口内同样能撞到 table-scan row-image decode 红格。** 这里有一个很有价值的 negative control:在同一 failpoint pause 窗口里把 worker 数降到 0、只保留 `prefill=128` 的静态旧行，再从外部手工执行默认 `DELETE` 和 `IGNORE INDEX` 的 `DELETE`，两者都 GREEN；因此“表里存在未 backfill 的旧行”本身不是充分条件，bug 仍需要并发 workload 把某个瞬时坏行像制造出来。第二，既然 access path 不是唯一驱动，那 delete 本身是否已经足够？于是又把 harness 扩成 `-skip-insert / -skip-delete`，专门隔离 DML 成分。在同样的高压红格上，打开 `-skip-insert=true` 让 worker 只做 delete，结果连续两轮都 GREEN；证据文件 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-deleteonly-green-20260711.log` 记录 `CONFIG ... skip_insert=true ...`，最终 `GREEN ... insert_ops=0 delete_ops=799 ... final_rows=0`，另一次 rerun 也同样 `insert_ops=0 delete_ops=838 ... GREEN`。这把 selector 再收紧一层: **单纯 delete storm 不够，insert+delete 往返大概率是制造坏 row image 的必要燃料之一。** 综合 07-11o 与这轮新边界，当前最好的 severe 解释不再是“public index lookup 读到了未 backfill 的 changing column”，而是更具体的 family 假设:**modify-column write reorg 窗口里，secondary-index family 存在时，insert/delete 往返会把某些行推进到一个临时坏 row-image 状态；一旦 delete 路径 later 触达这批行，无论走 `IndexLookUp -> TableRowIDScan` 还是强制 `TableFullScan`，都会在 cop table scan executor 上报 `missing data for NOT NULL column`。** 方法论收获也很直接:1) 当一个 severe lane 已经能稳定红时，下一步最高性价比不是继续堆随机 workload，而是**给 probe 加最小控制旋钮**(`delete hint`,`skip-insert/delete`) 去拆 controlling variable。2) 要区分“窗口态本身就错”与“并发 workload 制造了新的坏态”，静态 pause + 外部手工 SQL 是很好的 negative control。3) 当前 deterministic 程度仍然有限:`delete_hint=ignore` 有 red 也有 green，所以 seed 固定的只是 workload，不是调度；后续如果要继续逼近 root cause，更值得加的是对“哪一行在何时第一次变坏”的观察，而不是再扩更大矩阵。

**2026-07-11o issue62531 severe lane 从“能打红”推进到“能解释”: 当前最强解释是 modify-column write reorg 窗口里的 `IndexLookUp -> TableRowIDScan` 瞬时 row-image decode 失败,而不是 DDL 结束后的稳定坏表。** 这轮继续严格停留在 `target.seed.issue62531.modify-column-row-image.v1 / DDL_ROW_IMAGE_RECONSTRUCTION`，没有再发散。先对上轮自认为“deterministic”的 seed 结论做了诚实回归:同样 `with-index=true,prefill=120000,workers=16,max_val0=1000,seed_base=1,rows_per_op=200,hold=15s,post_release=20s`，在新 schema `ai_native_issue62531_diag_seed1` 上竟然跑成 GREEN；但把 overlap 再往上推到 `hold=20s,post_release=40s` 后，在 `ai_native_issue62531_diag_seed1_hold20` 又重新 RED，命中 `worker 8 delete path hit issue62531 signature on [875..74]: ... table_scan_executor.rs:467 ... missing data for NOT NULL column (offset = 2)`。这说明 **seed 只固定了 workload，不足以固定真正决定红格的调度因子**；当前 controlling variable 里除了值域/重复度/索引参与，还显然包含了 DDL apply window 与 cop task 调度对齐。随后对这次 red 做了两类“实锤”补证。第一类是**结果态 vs 窗口态分离**：red 后同一个 schema/table 的 DDL job `2043` 最终仍 `synced`，`SHOW CREATE TABLE` 正常变成 `val0 varchar(16)` 且保留 `KEY val0_idx(val0)`，`ADMIN CHECK TABLE` 通过，抽样 index/table 两条读路径计数一致，说明这条 severe bug 的用户层主要症状不是“DDL 结束后永久坏表”，而是 **online DDL 窗口内普通 DELETE SQL 会瞬时失败**。第二类是**pause-state 元数据/plan 抓取**：在新的长 hold schema `ai_native_issue62531_pause_meta` 上，pause 期 `information_schema.columns` 和 `SHOW CREATE TABLE` 都表明用户可见 schema 仍是旧的 `val0 int` + 公共 `val0_idx`；但同一时刻 `EXPLAIN DELETE FROM rows WHERE val0 IN (...)` 已经走成 `IndexLookUp`，具体是 `IndexRangeScan(Build) -> TableRowIDScan(Probe)`，而 `IGNORE INDEX(val0_idx)` 的 sibling 则是 `TableReader -> TableFullScan`。这和 live 红格里的报错栈 `components/tidb_query_executors/src/table_scan_executor.rs:467` 精确对上，等于把 selector 从“有索引时 broad delete 更容易红”进一步收窄成 **有索引时 delete 进入 index-lookup probe-side table row decode，而这个 probe 在 modify-column write reorg 窗口里可能读到缺失 NOT NULL 列的瞬时坏 row image**。更关键的是，在这个 pause-meta run 里，worker 自身后来又再次 RED(`worker 1 ... [95..294] ... missing data for NOT NULL column`)，说明这不是单次偶然。还专门做了一个用户层 control：在 pause 期从外部手工对几个历史候选窗口 (`101..300`,`153..352`,`632..831`,`875..74`) 连续执行 `BEGIN; DELETE ...; ROLLBACK;` 12 轮，全部未中；但同一 run 内部 worker 最终仍中红。这个负边界非常有方法价值：**具体值窗口本身不是充分条件，还需要 delete 触发时机与 reorg/apply/coprocessor probe 时机对齐**。因此 LOOP 需要补一个新的固定动作：一旦 live red 先被打出，不要只做 seed replay；必须立刻抓 pause 期的用户可见 schema、plan shape、以及 red 后结果态。对这类 online DDL 瞬时 bug，`ADMIN CHECK`/最终 rowset 只能说明“结束后自愈”，不能替代窗口内强 oracle。下一步最值得做的不是继续盲扫更大的矩阵，而是围绕 `IndexLookUp/TableRowIDScan` 这条具体链路，回读 `DELETE planner -> IndexesRowLayout -> RemoveRecord -> row decode`，并考虑加极窄 instrumentation 来验证是 probe-side row decode 读到了过渡态 row image，还是 delete/index-layout 路径提前构造了不完整行数据。

**2026-07-11n issue62531 severe lane 被新 oracle 重新打红,当前 master 不再是“直接邻域全绿”:**这一轮没有再发散去别的模块,而是严格沿 `store.py next` 选出的 `target.seed.issue62531.modify-column-row-image.v1 / DDL_ROW_IMAGE_RECONSTRUCTION` 继续压。关键变化不是盲目加行数,而是先承认前面的 GREEN 大多还是弱 oracle:它们主要验证了 `ADMIN CHECK`、count、少量 point/sample 行,却没有真正复刻 historical issue 里那个**prepared DELETE / full-row reader 解码整行**的用户症状。于是直接在既有 live harness `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go` 上补了两个新 oracle:1) server-side prepared `DELETE ... WHERE val0 IN (100 placeholders)`;2) prepared `SELECT id,val0,val1,padding ...` 的 full-row reader,要求每一行都能成功 decode 且满足 `CAST(val0 AS signed) * 10 = val1`。先做中压校准:无索引 `prefill=60000,workers=16,hold=15s,post_release=20s` 绿(`.../modify-column-pinned-broad-prepared-nonindex-live-20260711.log`),带索引同配置也绿(`.../modify-column-pinned-broad-prepared-index-live-20260711.log`),说明“新 oracle 挂上去”本身不会凭空制造假红。然后切到更像历史事故的高压格:带索引、`workers=16`,`hold=20s`,`post_release=40s`,`prefill=120000`。这里 current failpoint owner lane 在 **不需要额外 oracle worker** 的 broad workload 自身上就稳定 RED:日志 `.../modify-column-pinned-broad-index-nooracle-20260711.log` 命中 `worker 1 delete path hit issue62531 signature on [378..477]: Error 1105 (HY000): [components/tidb_query_executors/src/table_scan_executor.rs:467]: Data is corrupted, missing data for NOT NULL column (offset = 2)`;带 oracle 的 rerun 也分别在 `...prepared-index-heavy-rerun-20260711.log` 与 `...prepared-index-lowworker-20260711.log` 稳定复现同类 red signature。更有方法价值的是边界压缩:在同一 `with-index,workers=16,hold=20s,post_release=40s` 矩阵下,`prefill=90000` 绿(`...nooracle-90000-20260711.log`),`prefill=100000` 绿(`...nooracle-100000-20260711.log`),但 `prefill=120000` 红;而**去掉索引后同样 `prefill=120000,workers=16` 又回到绿**(`...prepared-nonindex-heavy-20260711.log`)。这把 selector 又收窄了一层:当前 current-master red shape 不是“任何 broad delete/insert overlap 都会炸”,而更像 **`MODIFY COLUMN` 涉及 indexed row-image/index rewrite 时,在足够大的 duplicate-rich workload + pinned active rewrite window 下,delete/table-scan path 会读到缺失 NOT NULL 列的损坏行像**。方法论收获非常直接:1) 前面的 GREEN 之所以把人带偏,不是 lane 错了,而是 oracle 太弱;`COUNT(*)`、`ADMIN CHECK`、点查/只取 `id` 的 query 不足以覆盖 reader-side row decode 义务。2) “命中窗口”与“判红路径”必须分层看待:已有 live `/fail/` hold 已经证明 overlap 命中是真的,这次真正把 lane 打开的，是把后置动作换成 **prepared delete/full-row reader**。3) hit red 后不要继续宽泛 fuzz;当前最小可解释矩阵已经是 `with-index + prefill≈[100000,120000] crossing + broad delete/insert workload + active apply-window hold`。下一步优先做两件事:一是把这条 red 收敛成本地/RTK 更小但仍保留 full-row decode 的 probe,二是围绕“为什么带索引红、去掉索引绿”去反推更细的 root-cause owner(更偏 row-image reconstruction 还是 index-rewrite/reader bridge)。

**2026-07-11m distributed add-index liveness lane 又收紧了一层: single-shot checkpoint/TS blip 是 GREEN,SQL semantic runtime error 也是 GREEN rollback,真正的红线更像“persistent generic post-import error 会把 DXF task 卡在 running,外部解除后立刻恢复”。** 这轮专门围绕 `read-index local ingest -> import -> checkpoint/next-TS` 边界做了一个很小但信息量很高的 live red/green gate。第一格,在 testbed `8220955` 的 failpoint owner 上打开 `forceSyncFlagForTest=return(true)` + `mockAfterImportAllocTSFailed=1*return`,对 `ai_after_import_ts_probe.t` 执行 `ALTER TABLE ... ADD UNIQUE INDEX idx_b(b)`;结果是 GREEN:job `1813` 最终 `synced`,说明 **单次 after-import TS/checkpoint 抖动** 在 current code 上能自己恢复。第二格,把同一个 failpoint 改成 persistent `return(true)` 后,`ai_after_import_ts_persist_probe.t` 的 job `1823` 与 DXF task `180009` 在超过 15s 的观察窗口内都维持 `running/step=1`,`RowCount` 卡在 `299`,前台 `ALTER TABLE` 一直不返回;一旦外部 `DELETE` 掉 failpoint,job 立刻在 `2026-07-10 20:02:04 UTC` 进入 `synced`。这说明 **persistent unknown/generic post-import error 仍会被留在 running/retry 环里**,用户层表现就是 DDL 挂住等待外部干预。第三格补了一个非常重要的 green control:直接用真实 SQL runtime semantic error 去打 sibling lane,`ai_funcidx_div0_probe.t` 上 `ALTER TABLE t ADD INDEX i((100/a))` 在 `a=0` 时没有挂住,而是 job `1818` 直接 `rollback done`,前台返回 `ERROR 1365 (22012): Division by 0`。这把 selector 收得更窄了: **不是所有 runtime error 都会触发 hanging family;SQL/terror semantic error 这边是 green,红的更像 infra/generic error bridge**。随后又做了一次更激进的 natural-shape 尝试:在 failpoint owner 临时加了 `pkg/ingestor/ingestctrl/pauseAfterSetTSBeforeImportEngine`,把 `ai_engine_missing_probe.t` 的 DDL 精确卡在 `SetTSBeforeImportEngine` 之后,手工删掉 `/tmp/tidb/tmp_ddl-4000/1828/...` 下的 local engine 目录再放行。这个实验拿到了真实 stderr:`open ... 000005.sst: no such file or directory`,证明 local-engine-missing 这类自然错误窗口确实能被撬开;但当前结果还不能算 confirmed red,因为 job `1828` 最终仍 `synced`,只是中间 owner/port-forward 发生了中断。这个负边界同样值钱: **简单删 local engine dir 还不足以稳定打出 hanging bug**,后续如果要继续逼近 natural trigger,更值得瞄准 `engine not found in SetTSBeforeImportEngine`,`local backend not found`,`external engine not found` 这类源码里的 plain fundamental error,而不是只删磁盘文件。

**2026-07-11l distributed add-index 又补出一个更像 severe/major 的 live liveness lane: run-time deterministic error 会把 DXF task 卡在 running/retry 里,而 setup-time error 只会立即 rollback。** 在追 `issue68828` 这类 stale backfill task meta 的过程中,这轮先做了一个非常重要的层次分离。第一步,在 failpoint owner worktree `/private/tmp/fp-build-5c9198/pkg/ddl/backfilling_dist_executor.go` 临时加了 `mockAINativeDistIndexInfoNotFound`,专门模拟 `newBackfillStepExecutor` setup 阶段的 `index info not found`。live 结果是: `ai_dist_stale_meta_probe.t1` 的 `ADD INDEX` job `1783` 直接 `rollback done`,用户层报 `ERROR 1105 (HY000): index info not found: -1`,而且 `mysql.tidb_global_task` 里看不到常驻 running task。这个负边界很关键,因为它说明 **“setup-time step-executor error” 本身不会自动形成 hanging task family**。第二步先专门补了一个 cancel negative boundary,用 `mockWriteLocalError=return(true)` 把 dist backfill 卡在 run-time retry 状态后再执行 `ADMIN CANCEL DDL JOBS 1798`。当前 master/5c9198 lane 下这条链是 GREEN:job `1798` 很快从 `cancelling` 进入 `rollback done`,DXF task `180004` 则从 `running -> cancelling -> reverted`,用户层返回 `ERROR 8214 (HY000): Cancelled DDL job`。这说明 **普通 run-time hang + admin cancel** 在 current code 上已经能正确清理,与历史 `issue64129` 的“rollback done 但 task 仍 running / goroutine leak”形状不同;这也和源码/历史修复对得上,因为 `ddl: cancel the job context before rolling back (#64130/#64202)` 已在 current tree 中。真正更危险的 lane 出现在第三步:继续用现成 dist backfill run-time failpoints 去打 **已进入 read-index subtask 的 deterministic error**。在 testbed `8220955` 上打开 `tidb_enable_dist_task=on` + `tidb_ddl_enable_fast_reorg=on` 后,两格 live probe 都打出了同一个结构性信号:1) `mockScanRecordError=return(true)` 时,job `1788` 对应 DXF task `180002` 在 15s 观察窗口内始终 `running`;用户层 `ALTER TABLE t1 ADD INDEX idx_b(b)` 一直不返回,直到外部清掉 failpoint 后才在 1 秒内 `synced`;2) `mockWriteLocalError=return(true)` 时,job `1793` / task `180003` 也同样在 15s 观察窗口内保持 `running`,`ALTER` 被卡住;清掉 failpoint 后很快 `synced`。更强的是 owner 日志里的**自相矛盾证据**:对 task `180002/180003`,`pkg/lightning/common/retry.go` 先明确记录 `meet un-retryable error`(`mock scan record error` / `mock write local error`),但随后 `pkg/dxf/framework/taskexecutor/task_executor.go:771` 又立刻把同一个错误当成 `meet retryable error`;接着出现一串重复模式:`run subtask failed -> meet retryable error -> subtask in running state and is idempotent -> run subtask start`。这正好把 generic root cause 钉在 **DXF backfill run-time error classifier** 上: `backfillDistExecutor.IsRetryableError` 里的 `common.IsRetryableError(err) || isRetryableError(err, true)` 让 unknown deterministic fundamental error 也跌进 retry bucket,于是 subtask 状态不进入 failed/revert,task 会一直留在 `running`。这条线比前面的 common reorg retry-family 更接近 severe/major,因为用户层表现不是“一次性回滚报错”,而是 **DDL 直接挂住等待,后台 task 持续自旋,需要外部干预(清 failpoint/清任务) 才能脱困**。质量判断仍要诚实:当前自然 trigger 还没有完全压实,这轮用的是 existing failpoint 来证明 classifier gap,而不是已经找到一个新的无注入真实触发器;但它已经把 `issue68828` 从“单个 stale EleIDs 特例”提升成一个更广的 family 假设:**只要 distributed backfill run-time 某处冒出 deterministic unknown error,当前 classifier 就可能把 DDL 卡在 running/retry 里。** 下一步最值得做的不是再扩 mock error 矩阵,而是沿这条 family 去找 natural trigger:比如 `stale EleIDs`、`local backend not found`、`external engine not found`、`unexpected EleIDs count` 这类源码里本来就可能返回 plain fundamental error 的路径。

**2026-07-11k retry-classifier lane 已从“单个 gRPC 文案”升级成可复用的 foreign-error family,并补出一个很强的 green control:**在 `8220955` 的 failpoint owner `fp-tidb` 上,这轮没有继续堆 topology chaos,而是先把 bridge-level one-shot 注入参数化:在 `/private/tmp/fp-build-5c9198/pkg/ddl` 新增 `buildAINativeClassifierErr(shape)`，并给 `runReorgJob` 返回后、进入 classifier 前的两条 bridge 都加上动态 failpoint:`mockDDLAddIndexClassifierErr` / `mockDDLModifyColumnClassifierErr`。随后直接做一个 6 格最小 live 矩阵,问的不是“还能不能再复现一次 grpc unavailable”,而是**这个 split 到底是单个字符串偶然命中,还是一整类 foreign transient error 的系统性分流**。结果非常干净:1) `context_deadline_exceeded` 是强 green control,`ADD INDEX(t_add_ctx, job 1755)` 与 `MODIFY COLUMN(t_mod_ctx, job 1758)` 都 `ErrCount=1 -> sleeps a while then retries it -> synced`;2) `driver_bad_conn` 命中明确 split:`ADD INDEX(t_add_badconn, job 1761)` `synced`,而 `MODIFY COLUMN(t_mod_badconn, job 1764)` 用户层直接报 `ERROR 1105 (HY000): driver: bad connection`,job 终态 `rollback done`;3) 更贴近真实网络层的 `net_conn_reset` 也同样 split:`ADD INDEX(t_add_reset, job 1767)` `synced`,而 `MODIFY COLUMN(t_mod_reset, job 1770)` 直接报 `ERROR 1105 (HY000): read tcp: connection reset by peer`,job 终态 `rollback done`。owner 日志把分叉也钉得很死:对 add-index 三格,都先记录一次 `run DDL job error` + `ErrCount:1` + `run DDL job failed, sleeps a while then retries it`,随后重跑并 `finish DDL job ... State:synced`;对 modify-column 的 `driver_bad_conn` / `net_conn_reset`,则是在 `modify_column.go:1409` 先打出 `run modify column job failed, convert job to rollback`,然后下一拍进入 `State:rollingback -> rollback done`。这条证据非常值钱,因为它把之前的 `grpc unavailable` 单点,升级成了一个更强的源码/行为结论:**`pkg/ddl/index.go:isRetryableJobError -> isRetryableError(err,true)` 会把 unknown foreign error 当成 retryable;而 `pkg/ddl/modify_column.go:isRetryableModifyColumnReorgJobError -> isRetryableError(err,false)` 会把同类 unknown foreign error 直接 fatal 化。**`context deadline exceeded` 之所以两边都绿,也不是偶然,而是它恰好被 `dbterror.ReorgRetryableErrMsgs` 明确收进了 shared retry set。方法论上,这轮正式沉淀出一个更强 LOOP 子套路:**先在源码里找 sibling module 的 classifier/rollback policy 差异,再把单点 bug lift 成 parameterized error-shape matrix;必须同时补一个 green control(这里是 `context_deadline_exceeded`),否则无法证明 harness/bridge 本身没有坏。**质量判断也要诚实:这轮仍主要证明的是 DDL availability / wrong-terminal-action family,不是 silent corruption;但它对“如何让 AI 高效地发现 bug”非常关键,因为它把 selector 从单 case 提纯成了可迁移的 family generator。 

**2026-07-11j classifier-family 又补出两块很关键的边界: index-family blast radius 扩到 `ADD PRIMARY KEY`,而 stock worker-level failpoint 仍然 live no-hit:**沿着上一条 `bridge-level semantic lift`，这轮没有换 lane，而是继续问两个更具体的问题。第一，`ADD INDEX` 的绿格究竟是不是只属于 add-index 本身，还是整个 index-family 的属性？于是直接复用相同 owner、相同 one-shot bridge 注入 `mockDDLAddIndexClassifierGrpcUnavailable=1*return(true)`，对 `ALTER TABLE t_pk ADD PRIMARY KEY(a)` 做 live probe。结果很干净:用户层 `ALTER` 成功，`SHOW CREATE TABLE` 出现 `PRIMARY KEY (a) NONCLUSTERED`，`ADMIN SHOW DDL JOBS` 里 job `1737` 最终 `synced`; owner 日志显示它先在 `run reorg job done` 之后吃到一次 `rpc error: code = Unavailable desc = mock ddl add-index classifier grpc unavailable`，记下 `ErrCount:1`，随后 `run DDL job failed, sleeps a while then retries it`，1 秒后重跑并 `synced`。这说明 **同一个 bridge fault 下,不仅 `ADD INDEX`，连 `ADD PRIMARY KEY` 也会走 retry-recover path**，而 `MODIFY COLUMN` 则仍然是 `rollback done`;因此这个 availability family 的边界可以更精确地写成: **index-family(ADD INDEX / ADD PRIMARY KEY) green, modify-column family red**。第二，既然 bridge-level 注入已经命中,那 stock 内建 worker-level `mockBackfillRunGrpcUnavailable` 现在是否终于能在 live owner lane 上命中？在修完 `/fail/` PUT 语义、failpoint binding refresh、以及 linux/amd64 owner rebuild 之后，又专门重跑了两格 live probe:`ADD INDEX + mockBackfillRunGrpcUnavailable` 和 `MODIFY COLUMN + disableLossyDDLOptimization + mockBackfillRunGrpcUnavailable`。两格都直接 GREEN:job `1733`(`add index`) 和 `1734`(`modify column`) 都 `synced`,`ErrCount=0`,`ADMIN CHECK TABLE` 绿,而 owner 日志里完全看不到 `mock backfill grpc unavailable`/rollback/retry 相关痕迹,只看到正常的 `backfill worker finish task -> run reorg job done -> synced`。这条负边界很值钱,因为它把之前的疑问彻底钉死了: **在 current master/5c9198 live owner lane 上,worker-level built-in failpoint 不是稳定可打的 selector;真正可复用、可实锤的注入高度仍然是 retry classifier bridge 本身**。方法论因此再次收敛:当 live worker failpoint 反复 no-hit 时,不要再堆更多 workload/行数/重试次数,应立刻上提到 bridge altitude,并用 sibling DDL 去做最小红绿矩阵;命中后再回头看更低层 stock failpoint 是否只是 observer debt/selector debt 的负边界,而不是继续误以为“产品 bug 不存在”。

**2026-07-11i common reorg transient-fault lane 已完成 live bridge-level semantic lift,并把“为什么之前 live 没打中”也补成了方法资产:**上一轮围绕 `mockBackfillRunGrpcUnavailable` 的 live 尝试其实卡在两层 debt 上:第一,我们一度把 runtime `/fail/` 当成“URL 路径赋值”,但正确姿势是 `PUT -d '<action>' /fail/<full-name>`;第二,worker-level failpoint 是否真的覆盖当前 live shape,会被源码版本/路径分叉影响。于是这轮没有继续在 worker 层纠缠,而是直接复用已被证明有效的 **bridge altitude** 做法:在 testbed `8220955` 的 failpoint owner worktree `/private/tmp/fp-build-5c9198` 里,给 `pkg/ddl/index.go:runReorgJobAndHandleErr` 新加极窄 `mockDDLAddIndexClassifierGrpcUnavailable`,给 `pkg/ddl/modify_column.go` 新加对称的 `mockDDLModifyColumnClassifierGrpcUnavailable`,两者都只在 `runReorgJob` 返回后、进入各自 retry classifier 前注入一次 `rpc error: code = Unavailable`。中间又踩出两个非常值钱的 infra debt 并修掉:1) **改完 failpoint 源码后必须把 failpoint state 真正 disable->enable 一次**,否则新名字不会进入绑定;2) **回灌到 pod 的 owner binary 必须重新用 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 构建**,不然会得到 `/fp: cannot execute binary file` 这种纯环境假红。把这些债清完后,live 小矩阵终于很干净:1) `ADD INDEX + 1*mockDDLAddIndexClassifierGrpcUnavailable + fast_reorg=off/dist_task=off` GREEN,用户层 `ALTER` 返回成功,`SHOW CREATE TABLE` 可见 `KEY idx_b (b)`,`ADMIN CHECK TABLE` 通过;owner 日志里 job `1723` 先记一次 `ErrCount:1` 和 `run DDL job failed, sleeps a while then retries it`,再在 1s 后重跑并 `synced`;2) `MODIFY COLUMN + 1*mockDDLModifyColumnClassifierGrpcUnavailable + disableLossyDDLOptimization + fast_reorg=off/dist_task=off` RED,用户层直接报 `ERROR 1105 (HY000): rpc error: code = Unavailable desc = mock ddl modify-column classifier grpc unavailable`,`SHOW CREATE TABLE` 仍保留原列类型 `val0 int`,`ADMIN SHOW DDL JOBS` 里 job `1726` 终态 `rollback done`。更关键的是,两格在 owner 日志里都证明 **底层 reorg 本身已经跑完**:job `1723/1726` 都先出现 `run reorg job done` / `backfill worker finish task added count=128`,随后才在 classifier bridge 分叉;`1723` 走 `retry -> synced`,`1726` 走 `convert job to rollback -> rollback done`。这比此前的 local-only differential 更强,因为它已经是 **live owner lane 上、同一 bridge、同一 fault family 的 sibling red/green split**。质量判断上,它仍主要是 DDL availability 而不是 silent corruption,但已经符合当前 severe 主线里的高价值目标:一个长时间 `MODIFY COLUMN` 在一次性 retryable foreign error 面前会直接失败回滚,而 sibling `ADD INDEX` 在同桥上能够恢复。方法论升级也很明确:当 worker-level live-lift 不稳定时,不要继续堆更粗 chaos,而是**把 fault 上提到 classifier/bridge,再用 sibling DDL 做最小红绿矩阵**;同时把 `/fail/` 控制语义、failpoint binding 刷新、以及 linux/amd64 owner rebuild 记成固定操作清单,这样后续 agent 才不会被同一类假阴性/假红反复拖住。

**2026-07-11h 并发 severity screen 与 checkpoint TODO 各补出一个很有价值的负边界:**为了防止把 `issue59701` 这条新红格误升成 severe,这轮没有继续围着 false-duplicate 本身打转,而是把 silent/liveness consequence gate 补齐。先把 `/Users/bba/pc/ai_native_concurrency_harness.sh` 升级成可通过环境变量指定 `MHOST/MPORT`、`DDL_FAST_REORG/DDL_DIST_TASK`、以及 `DML_FEEDERS` 多 feeder 并发,再新增 case `/Users/bba/pc/ai_native_case_add_unique_hot_reinsert.sh`，专门复刻 `ADD UNIQUE INDEX + hot delete/reinsert on indexed key`。在同一 testbed owner lane 上跑了两组 screening:1) `DML_FEEDERS=1` 单轮 sanity GREEN;2) `DML_FEEDERS=8` 的 4 轮 widened-substate run 也全部 GREEN,既没有 `RED_WEDGE(O28)`，也没有 `ADMIN CHECK`/hot-key invariant 红点。这个结果很重要,因为它说明**当前这条 unique false-duplicate lane 还没有被 generic concurrency harness 升级成 C3 silent-corrupt 或 liveness wedge**;它更像一个需要精确 phase-aware probe 才能稳定命中的 moderate wrong-error,所以主线不该继续在这里耗 severe 预算。随后又顺着 `tests/realtikvtest/addindextest3/ingest_test.go:537` 那个 TODO("checkpoint 还缺 scan ts / import ts / key range,否则无法 idempotent re-ingest") 做了一个 live source-check:直接在 failpoint owner 上打开 `mockAfterImportAllocTSFailed=2*return`,对 3 万行、24 region、`fast_reorg=on/dist_task=off` 的表执行 `ALTER TABLE ... ADD UNIQUE INDEX idx_k(k)`，终态是 GREEN:`ADMIN CHECK TABLE` 绿,table/index count 都是 `30000`。这意味着**checkpoint TODO 不是一个拿最小 live SQL 就能直接打红的 trivially-open cell**;后续如果要继续追这条 lane,必须重新补上它在本地 realtikv test 里依赖的条件(如更强 phase control / ForceSync 等价物 / concurrent DML / 更精确 checkpoint observer),不能把 TODO 文本直接当作 live bug 证明。

**2026-07-11g issue59701 lane 被重新压成一个最小红绿矩阵,但质量判断应诚实降回“wrong-error/availability”,不要误当 severe data-corruption:**此前 `OWNER_TOPOLOGY_HANDOFF` 的 coarse topology fault(`PD bounce`,`owner handoff`,`multi-index/multi-schema`) 已经在 live cluster 上反复 GREEN,所以这一轮没有继续堆更粗 chaos,而是回到源码/注释/历史行为里找真正的证明义务:当前 add-index 路径明说“对这些已覆盖行上的 update/delete/insert,TiDB can handle it correctly”,而 `pkg/ddl/tests/indexmerge/merge_test.go` 里也已有 `TestAddUniqueIndexFalsePositiveDuplicate` / `TestAddIndexMultipleDelete` 这类 sibling contract。基于这个义务,我们把 `/Users/bba/pc/ai-native-probes/add_index_owner_fault_oracle_probe.go` 补成 `ddl-shape={single,unique,multi}`，然后在 testbed `8220955` 的 failpoint owner `fp-tidb` 上做三格最小矩阵:1) `UNIQUE + concurrent delete/reinsert on indexed key` 稳定 RED,用户层直接报 `ERROR 1062 Duplicate entry 'hot-xxxx' for key 'rows.uk_c'`,job 终态 `rollback done`;2) `UNIQUE + no DML` GREEN,DDL `synced`;3) `NON-UNIQUE + same concurrent DML` GREEN。更关键的是,最早帮助我们“看到红色”的那组 failpoint(`mockAfterImportAllocTSFailed`,`resignAfterFlush`,`ownerResignAfterDispatchLoopCheck`) 在全部清掉之后,这个 `UNIQUE + DML` 红格仍然存在,说明真正最小触发条件不是 topology fault,而是 **fast-reorg/local-ingest unique duplicate-check 对 delete/reinsert 语义的判定**。终态强 oracle 也已经补齐:rollback 后表上无新索引可见、`select c,count(*) ... having count(*)>1` 查不到重复、hot keys 计数都为 1、`ADMIN CHECK TABLE` GREEN。当前最可信的源码归因是 `pkg/ddl/index.go:checkDuplicateForUniqueIndex -> pkg/ddl/ingest/backend.go:CollectRemoteDuplicateRows` 这条 post-backfill duplicate-proof 路径过度近似了 delete/reinsert 语义;这还是**source-backed inference,不是 line-by-line root cause proof**。方法论上,这轮很值钱,因为它把 `issue59701` 从“继续打 topology 大锤”纠偏成了一个可复用套路:**先用更重 fault 打开 suspect lane,一旦看到红格,立刻把 fault 一项项拿掉,逼出真正 selector 与最小触发条件**。但质量判断也必须诚实:这个新红格目前更像 online DDL wrong-error / availability bug,不是 silent corruption 或 admin-check 级 severe data bug,所以下一步不应围着它继续做 blast-radius 枚举,而应把它当作“方法成立”的新证据,随后把主火力重新拉回更高严重性的 DDL lane。

**2026-07-11f issue62531 再补两轮 pinned broad live workload,负边界明显变硬:**上一轮 128 行 apply-window live probe 证明了“简单 stale-row-image overwrite”不成立,但还可能被质疑为 workload 太玩具。这一轮专门补了一个更贴近历史事故的宽 workload probe `/Users/bba/pc/ai-native-probes/modify_column_pinned_broad_delete_scan_probe.go`:它不是单次 `DELETE ... WHERE val0 IN (1..32)`，而是直接复用 historical `issue62531` 风格的多 worker insert/delete 周期,同时把 `MODIFY COLUMN val0 int -> varchar(16)` 精确钉在 live `beforeUpdateColumnBackfillApply` 窗口里,并且通过 `/fail/` 额外打开 `disableLossyDDLOptimization=return(true)` 保证走 row-reorg 路径。第一轮中压配置:`prefill=60000`,`workers=16`,`hold=15s`,`post-release=20s`。证据日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-broad-delete-scan-live-20260711.log` 先记录 `PREFILL rows=60000`,随后 `PAUSED hold=15s workers=16 prefill=60000`,说明 DDL 确实在精确 apply-window 被卡住;放开后又记录 `DDL released and finished`,终态 `GREEN workers=16 ... insert_ops=523 delete_ops=514 insert_errs=0 delete_errs=0 final_rows=444`。随后再做一轮更重配置:`prefill=120000`,`workers=24`,`hold=20s`,`post-release=30s`,`timeout=12m`。证据日志 `/Users/bba/pc/ai-native-assets/logs/modify-column-pinned-broad-delete-scan-live-heavy-20260711.log` 同样先命中 `PAUSED`,再 `DDL released and finished`,最后 `GREEN workers=24 ... insert_ops=868 delete_ops=852 insert_errs=0 delete_errs=0 final_rows=1155`。这两轮都不是“没有 active overlap”的假绿:hold 点就是 `updateColumnWorker.fetchRowColVals -> apply txn.Set` 之间,而且 DML worker 在 hold 期间持续跑。方法论上,这把 `issue62531` severe lane 的 fault-shape 假设又剥掉一层:当前 master/5c9198 上**不仅简单 delete-scan 插窗不会红,就连更像历史事故的 broad delete-scan 周期 workload,在精确 apply-window hold 下也仍然不红**。因此下一步不该再在这条 lane 上继续盲加行数/worker/hold 时间,而应该切换到更贴近真实事故根部的 shape:1) reader/table-scan executor 侧更真实的 scan path 或 prepared delete path;2) delete + reinsert + repeat modify 循环的更长生命周期 shape;3) 重新审视 historical issue 环境差异(8.5.2 vs current master)是否包含 reader 组件或非 DDL owner path 的行为变化。与此同时,方法资产上又多了一个很重要的分层经验:**“broad workload”本身不够,还必须证明 broad workload 在正确 bridge altitude 命中**;这次通过 live `/fail/` hold 已经做到了这一点,所以这两轮 GREEN 应视为强负边界,不是普通 smoke GREEN。

**2026-07-11e issue61255/issue62531 severe lane 继续收口,并补出一个 live negative boundary:**这一轮先没有盲目换 lane,而是把 severe seed 里最容易“看起来像红、其实早被修掉”的两条线压实。第一,`issue61255` 的 mixed-owner temp-index merge 先做源码义务核对:当前 `writePhysicalTableRecord` 在 merge 阶段已经显式调用 `getSplitKeysForTempIndexRanges`,按每个 temp index 前缀切 range;`mergeIndexWorker.BackfillData` 进入任务后也会先用 `findIndexInfoByDecodingKey(taskRange.startKey)` 绑定当前 index owner。再叠现有 probe `go test --tags=intest ./pkg/ddl/tests/indexmerge -run TestAINativeMVIMergeOwnerHomogeneityProbe -count=1`,结果稳定 GREEN,说明 `issue61255` 在 current 实现上不是“仍可能混 owner 的开放 severe lane”,而是**源码证明 + 探针验证一致的绿边界**。第二,围绕 `issue62531` 我们没有再打粗 owner/network chaos,而是在源码里新加了最窄 hold 点 `pkg/ddl/column.go:updateColumnWorker.BackfillData` 的 `beforeUpdateColumnBackfillApply`。本地先用 `go test --tags=intest ./pkg/ddl -run 'TestModifyColumn(DeleteScanDuringBackfillApply|DeleteReinsertAfterBackfillCommitted)' -count=1` 验证:post-commit 绿样本继续绿,更窄的 `DELETE ... WHERE val0 IN (...)` apply-window probe 也绿。随后把同一个 hold 点 lift 到 testbed `8220955` 的 failpoint owner `fp-tidb`:注意这里又踩出一个重要方法论细节,**本地 `EnableCall` 型 hook 不能直接当 live `/fail/` 注入点**,要改成 `failpoint.Eval` 才能被 runtime `/fail/` 真正 pause。修完后,在同 commit worktree `/private/tmp/fp-build-5c9198` 重新编 failpoint `/fp`,起 owner lane,再跑新的 live probe `/Users/bba/pc/ai-native-probes/modify_column_apply_window_live_probe.go`。这次 probe 先把 `beforeUpdateColumnBackfillApply` 设成 `pause`,对 128 行、batch=32 的 `MODIFY COLUMN val0 int -> varchar(16)` 观察到 **`PAUSED inferred_by_runtime=true wait=2s`**,说明 live hook 真的命中;随后在精确窗口里执行 `DELETE FROM ... WHERE val0 IN (1..32)`,放开 failpoint 后终态是 `GREEN table=ai_native_issue62531_apply_window.rows rows=128 batch=32`,`ADMIN CHECK TABLE`、row-count 和 indexed-vs-table oracle 全绿,证据日志在 `/Users/bba/pc/ai-native-assets/logs/modify-column-apply-window-live-probe-20260711.log`。这条负样本很关键:它说明 `issue62531` 在 current master/5c9198 live owner lane 上**不是“fetch 旧 row image -> delete-scan 插进来 -> apply 写回脏 row bytes”这种简单 stale-row-image overwrite 形状**。下一步如果继续追这条 C3 lane,应该转向更贴近真实事故 workload 的 shape:多 worker insert/delete 周期、delete-scan 覆盖更长窗口、或 reader/scan executor 侧更真实的 table-scan path;同时方法论要把“本地 test hook”与“live dynamic hook”区分成两类资产,前者用 `EnableCall`,后者必须是 `/fail/` 可控的 `Eval/Inject`。

**2026-07-11d ingest retry-family severe lane 已完成 live semantic lift,并把 fault shape 精确收敛到“bridge 下绿 / bridge 上红”:**在 testbed `8220955` 上,先按集群同 commit `5c9198e948` 重建 failpoint owner lane:本地新建 worktree `/private/tmp/fp-build-5c9198`,编出 failpoint `tidb-server-fp`,起旁路 pod `fp-tidb`,确认 `/fail/` 可用后把 `tc.spec.tidb.replicas` 缩到 `0`,让 `fp-tidb` 成为唯一 TiDB + owner,再通过 `kubectl port-forward pod/fp-tidb 14000:4000 18080:10080` 跑 live probe。关键结果不是“打一针就红”,而是**同一错误家族在不同注入高度上出现明确分叉**:1) 现成 lower-bridge failpoint `github.com/pingcap/tidb/pkg/ingestor/ingestctrl/FailIngestMeta=1*return("notleader")` 和 `.../NoLeader=1*return(true)` 都能命中,`/tmp/fp.log` 分别记录 `[Ingest:NotLeader]` / `[KV:ErrNoLeader]` 与 `meet retryable error when ingesting/writing to TiKV`,但最终 `ADD INDEX` jobs `1548/1551` 都 `synced`,说明 **ingestctrl 内层 retry/rescan 能把这类错误吃掉**;2) 随后在 worktree 的 `pkg/ddl/index.go:runIngestReorgJob` 加极窄 failpoint `mockDDLIngestClassifierErr`,它只在 `runReorgJobAndHandleErr` 返回后、进入 `isRetryableJobError/isRetryableError` 前注入 `ErrNoLeader/NotLeader/RegionNotFound`。重新 `failpoint-disable -> failpoint-enable -> build -> redeploy` 后,bridge 上的 live red 已经补成一个小矩阵:`ADD INDEX + noleader(t4)`、`ADD INDEX + notleader(t8)`、`ADD PRIMARY KEY + noleader(tpk1)`、`ADD PRIMARY KEY + regionnotfound(tpk5)` 全部直接报错并 `rollback done`;同环境 green control `t5/t9/tpk2/tpk6` 在清空 failpoint 后全部 `synced`。owner 日志也稳定出现 `Unknown error class [class=KV/Ingest]`、`run reorg job failed, convert job to rollback`、以及 `State:rollingback -> rollback done`。这条 live semantic lift 非常关键:它证明**bug 不是“ingest mode 遇到 leader-change family 就挂”**,而是“当 retryable foreign error 越过 ingestctrl 内层恢复,直接落到 DDL retry classifier 时,被 fatal 化”。方法论因此再前进一步:live 注入必须按**bridge altitude** 分层,先区分 `below-bridge`(模块内自愈) 与 `at-bridge`(分类/持久化/rollback),再决定 failpoint/harness 应该落在哪一层。当前最值得复用的证据文件是 `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix.log` 与 `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix-extended.log`。

**2026-07-11c ingest retry-family severe lane 的第一轮 live-lift 已收口为绿边界,不是反证:**围绕 `ddl-ingest-retryable-kv-family-misclassified-fatal`，本轮没有继续放大外部 chaos,而是新建 active-window probe `/Users/bba/pc/ai-native-probes/add_index_tikv_bounce_oracle_probe.go`，专门把 `ADD INDEX` / multi-`ADD INDEX` 钉在 `write reorganization` 再做 TiKV pod bounce。probe 本身先补了两个 observer debt:1) `information_schema.ddl_jobs` 对 multi-schema parent/subjob 不能只看最新 job，现已优先 `job_type like 'add index%' + schema_state!=none + row_count`；2) 重复跑同表同 SQL 会被旧 history 污染，现已加 `job_id watermark before submit`。在 testbed `8220955` 上的三条 live lane 终态都为 GREEN:`single add-index + 1 TiKV bounce` 成功(`.../logs/ingest-live-tikv-bounce-single.log`),`multi add-index + 1 TiKV bounce` 成功(`.../ingest-live-tikv-bounce-multi.log`),`multi add-index + sequential dual TiKV bounce` 也成功(`.../ingest-live-tikv-bounce-dual-fixed-watermark.log`),最终 indexes/counts/DDL history 都对上。这条结果很关键:它**没有推翻 local confirmed 的 retry-classifier gap**,而是把当前 lane 明确标成 `LIFT_BLOCKED(fault-shape gap)`/`NEGATIVE_BOUNDARY`。也就是说,coarse pod-level leader churn 仍然太远,不足以把真实 `Ingest:NotLeader/RegionNotFound/KV:ErrNoLeader` 错误身份稳定送到 DDL retry bridge。下一步不要继续堆更粗的 pod/network chaos,而要升级为更贴近 classifier 输入的 fault shape:failpoint-enabled TiDB image、TiDB<->特定 TiKV 的更窄 gRPC drop/blackhole,或可控 region leader transfer/movement。

**2026-07-11b 新的更强 severe candidate 已浮现:ingest-mode DDL 对 retryable TiKV/ingest 错误域存在 shared fatalization gap。** 在上一轮 `TRANSIENT_FAULT_RETRY_CLASSIFIER` 资产化之后,这轮继续往错误域桥接里收窄,确认 `Ingest:NotLeader` / `Ingest:RegionNotFound` / `KV:ErrNoLeader` 这组三个 **Lightning/ingest 明确判为 retryable** 的 transient family,到了 DDL reorg retry gate 却被当成 fatal。最强证据不是单一 DDL,而是**跨操作复用同一个缺口**:1) 既有 `/Users/bba/pc/ai-native-assets/logs/ingest-retryable-family-outcome-probe.log` 已证明 `ADD INDEX` 与 `MODIFY COLUMN` 在这三类 fault 上都直接 `rollingback -> rollback done`(`1733/1738/1746`,`2668/2672/2675`,`5456/5461/5469`,`6393/6397/6400` 等);2) 新增 `/Users/bba/pc/ai-native-assets/logs/ingest-add-primary-key-retry-family-local.log` 进一步证明 **`ADD PRIMARY KEY` 也同样中招**,而且同一 DDL 对 `grpc unavailable` 是绿的: `grpc unavailable` 走 outer retry 并最终 `State:synced`(`1776`,`1861`,`alter err:<nil>@1866`),但 `Ingest:NotLeader` / `Ingest:RegionNotFound` 分别在 `2827/2840/2851`、`3805/3818/3829` 直接 rollback + SQL 报错。与此同时,`/Users/bba/pc/ai-native-assets/logs/ddl-retry-classifier-gap-local.log` 把分类断点钉死:`ingest_kv_not_leader` / `region_not_found` / `no_leader` 都是 `raw=false ddl_synth=false lightning=true`,而 `grpc_unavailable` 是 `raw=true lightning=true`。源码侧也对上: `pkg/lightning/common/retry.go` 与 `pkg/ingestor/ingestctrl/job_worker.go` 把这组错误当 retryable 处理,但 `pkg/ddl/index.go:isRetryableError` 只认 DDL reorg 自己的 msg/code,遇到 `Unknown error class [class=Ingest/KV]` 后直接跌出 retry 集合,随后 `runReorgJobAndHandleErr` / `runIngestReorgJob` 把 job 翻成 rollback。方法论上,这条 lane 比前面的 modify-column-only transient family 更强,因为它既跨 `ADD INDEX` / `ADD PRIMARY KEY` / `MODIFY COLUMN`,又更贴近真实 TiKV leader churn 语义。当前已新增草案 `/Users/bba/pc/ai-native-ingest-retryable-fault-rollback-draft.md` 与资产包 `/Users/bba/pc/ai-native-assets/ingest-retryable-family-results.jsonl`。下一轮最值得做的不是回旧 seed,而是沿这条共享错误域桥接缺口继续做 live-lift:要么 failpoint-enabled TiDB image,要么 active ingest reorg + 窄 TiKV leader churn,目标是把真实 `NotLeader/RegionNotFound/NoLeader` shape 打到 DDL retry bridge,而不是只做粗粒度 freeze。

**2026-07-11a transient retry lane 已沉淀成资产包,并补出 retry-log trap:**在前一轮 `issue1290001` 与 `modify-column` local differential 基础上,这轮把 `TRANSIENT_FAULT_RETRY_CLASSIFIER` 正式机器化到 `/Users/bba/pc/ai-native-assets/modify-column-transient-retry-results.jsonl`:新增 selector/oracle/scenario/schedule/module/obligation/fault/RED-GREEN run 资产,覆盖 `grpc unavailable` / `grpc dataloss` / `invalid connection` / `driver bad connection` / `conn reset` / `broken pipe` / `conn refused` 这组 transient family。最关键的新经验不是又多了一条红格,而是**outer retry log 不等于 recover**:`pkg/ddl/job_worker.go` 会在持久化 job state 之后打印 `run DDL job failed, sleeps a while then retries it`;对 `MODIFY COLUMN` 而言,inner `modify_column.go` 已经先把 job 翻成 `rollingback`,所以下一拍看到的是 `State:rollingback -> rollback done`。对应 preserved logs:`modify-column-transient-family-local.log:6598/6607/6610`,`10326/10335/10338`; sibling `ADD INDEX` 才是真正 `retry -> synced`(`1786/1831`,`5630/5675`)。方法论结论被进一步压实:**S24/O31 这条 lane 必须看 terminal state,不能只看 retry log 或 error text**。这让后续 session 在追 retry-classifier / single-hit terminalization 时,可以直接复用资产而不是重新解释这一层日志幻觉。

**2026-07-10ad severe seed queue 已激活:**severity admission gate 收紧后,`store.py next` 一度因为历史队列只剩 C1/C2 source target 而返回 `null`。现已新增 `/Users/bba/pc/ai-native-assets/severe-ddl-seed-intake-20260710.jsonl`,把三条 serious DDL lane 显式入库为 `C3_DIRECT` admitted target,并补了对应 selector/scenario/schedule/oracle/module_profile/obligation/fault 资产:1) `target.seed.issue62531.modify-column-row-image.v1` (`DDL_ROW_IMAGE_RECONSTRUCTION`) 聚焦 `MODIFY COLUMN` 与并发 DML 后 reader-visible row corruption/`missing data for NOT NULL column`;2) `target.seed.issue61255.multi-schema-temp-index-owner.v1` (`MULTI_ARTIFACT_OWNER_HOMOGENEITY`) 聚焦 multi-schema add-index mixed-owner temp artifact merge 后 `ADMIN CHECK TABLE` 失败;3) `target.seed.issue59701.add-index-topology-admin-check.v1` (`OWNER_TOPOLOGY_HANDOFF`) 聚焦 add-index active reorg 期间 topology fault 后 bad terminal state/`ADMIN CHECK`。当前调度器首选 lane 已变为 `issue62531 -> issue61255 -> issue59701`,对应 `next_state` 全为 `ready_to_execute`;这意味着后续 session 不需要再从 severity policy 空转到重新找方向,可以直接从 C3 lane 开始压小矩阵和搭 harness。

**2026-07-10ae fast-reorg PD transient-fault lane 命中新 confirmed 高危 DDL availability bug:**在 testbed `8220955` 上,把 observer 修正为“DDL session live + schema_state=write reorganization + min-running>=3s”后,`single add-index + fast_reorg=ON + dist_task=OFF + 2 PD bounces` 稳定打出用户层 `ERROR 1105 (HY000): create TSO stream failed, retry timeout`。这轮不是只停在 live RED,而是继续往源码和 job history 压:同日复查 `mysql.tidb_ddl_history` 的 jobs `1192/1204`,都显示 `err.rfccode=PD:client:ErrClientCreateTSOStream`,`err_count=1`,`is_fast_reorg=true`,`is_dist_reorg=false`,`state=3`;`ADMIN SHOW DDL JOBS` 终态都是 `rollback done`。sibling control `fast_reorg=OFF` 的 job `1196` 为 `synced` 且 comment 明确走 `txn` 路径。随后用本地最小 probe `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go` 直接构造 `ErrClientCreateTSOStream(... retry timeout)`,当前代码稳定打出 `Unknown error class [class=PD]` 且 `isRetryableError(...)=false`。这说明问题不是“retry 很多次还是失败”,而是 **foreign PD error 在 DDL retry classifier 里第一次就被当成 fatal**。远端 `found_bug` 已入库为 `id1290001` (`severity=high`,`status=confirmed`,`root_cause_id=addindex-fastreorg-pd-tso-retry-misclassified-fatal`),草案 `/Users/bba/pc/ai-native-fast-reorg-pd-tso-retry-timeout-draft.md`。方法论上,这是新的 `S24 transient fault retry classifier` + `O31 DDL retryable fault terminal oracle`:高效点不是继续堆 topology chaos,而是先证明 fault 打中 active window,再用 `err_count=1 + sibling green control` 把问题压缩成“恢复路径的错误分类/错误域桥接错了”。

**2026-07-10af common reorg transient-fault lane 再压出一个 module-differential availability gap:**把外部 chaos 收窄成内核级一次性 failpoint 后,在通用 backfill worker 注入单次 `gRPC Unavailable` (`/Users/bba/pc/tidb/pkg/ddl/backfilling.go` 的 `mockBackfillRunGrpcUnavailable`) 并做最小对照 probe(`/Users/bba/pc/tidb/pkg/ddl/ai_native_reorg_grpc_probe_test.go`)。结果非常干净:1) `ALTER TABLE ... ADD INDEX` 在同一单次故障下 `ErrCount=1` 后继续推进并最终 `synced/PASS`;2) `ALTER TABLE ... MODIFY COLUMN` 在同一单次故障下直接把 job 打成 `rollback done`,用户层返回 `[ddl:-1] rpc error: code = Unavailable desc = mock backfill grpc unavailable`。源码差异也直接对上:`pkg/ddl/index.go:isRetryableJobError -> isRetryableError(err,true)`，而 `pkg/ddl/modify_column.go:isRetryableModifyColumnReorgJobError -> isRetryableError(err,false)`。这不是 live testbed confirmed 的 C3 issue,但已经是一个很强的 **local confirmed DDL availability differential**:同样“一次性瞬时可恢复故障”,`add index` 走 retry path,`modify column` 走 rollback path。方法论增量:在 `S24 transient fault retry classifier` 下面新增一条更高效的 differential probe 习惯用法——**先找共享 phase/共享 worker,再对 sibling DDL 注入同一个 one-shot transient fault;若红绿分叉,优先追 module-specific retry classifier/rollback policy,而不是继续放大外部 chaos**。

**2026-07-10ag modify-column live-lift 负样本已收敛,并升级了 chaos harness:**围绕上面的 local differential,在 testbed `8220955` 上继续做 live lift,但这次不是盲目加大 chaos,而是把 fault shape 与 harness 都压实。第一步把 `/Users/bba/pc/ai-native-probes/modify_column_owner_fault_oracle_probe.go` 扩成 active-window fault harness,先打 `dual TiKV pod delete`(`tc-tikv-0,tc-tikv-1`,`wait-pod-ready=false`)；第一次 live run 里 DDL 本身继续推进,真正先红的是并发 DML 陪跑流量,报 `Region is unavailable`。于是先修 harness:把这类 topology-fault 下预期内的瞬时 SQL/KV 错误纳入 transient set,让 DML worker 重试而不是 `fatal`;同时新增 `fault-mode=network-partition`,直接用 Chaos Mesh `NetworkChaos` 在 active `write reorganization` 窗口把所有 TiDB 到指定 TiKV(`tc-tikv-0,tc-tikv-1`)做 `both` 方向 12s 隔离。修完后两条 live lane 都完整跑通且终态一致:1) dual pod delete 命中 active window 后,`MODIFY COLUMN` 最终 `synced`,`ADMIN CHECK TABLE` 和 point-row/index-path/delete-rollback oracle 全绿;2) 更细粒度的 `NetworkChaos` 让 row_count 在窗口内明显变慢,总时长拉长到 `1m48s`,但终态仍是 `synced` + final oracle green。方法论结论很关键:**local failpoint RED 不会自动 lift 成 live cluster RED**。当我们已经有“active window 命中 + sibling/shape 更贴近 + 强终态 oracle”仍然 live GREEN 时,应把该 lane 标成 `LIFT_BLOCKED(fault-shape gap)` 或 `NEGATIVE_BOUNDARY`,而不是继续无脑放大外部 chaos。下一步该追的是更贴近真实 classifier 输入的 fault shape:TiDB<->TiKV 更窄的 gRPC 级 fail/blackhole、failpoint-enabled live image,或能证明 `Unavailable` 真正冒出到 DDL classifier 的注入,而不是继续用删 pod 这类会先把陪跑流量打穿的大锤。

**2026-07-10ac 严重度目标重新收紧:**当前 LOOP 的主目标是挖掘可实锤的 C3 严重 bug:静默数据丢失/损坏、已发布约束被绕过、持久跨 session 状态泄漏，或用户操作被 DDL/txn 活性故障阻断。之前 LOOP 只有“consequence-first”排序，仍会让 C1/C2 方法论样本进入主线；现已在 `ai-native-autonomous-loop.md` 和 methodology-v2 增加 severity admission gate。今后只有 `C3_DIRECT`，或明确写出 C3 升级路径与强 oracle 的 `C2_WITH_LIFT`，才能跑 `MINE_BUG`；普通 wrong-error、metadata-only 和“Close 被跳过但未证明后续用户影响”的案例只能作为 oracle/selector 校准，不再作为公开战果或主线目标。`1260006/1260008/1260009` 保留在 bug 库和方法论证据中，但不代表本项目要追求的严重度标准；其中 `1260006` 只有完成 live owner-handoff -> stale reorg result -> user-visible DDL 错误/卡死的 lift，才重新进入主线。

**2026-07-10ab terminal-action lane 再命中一个当前新 bug,并反哺 selector:**在 `chunkWorker.Close` 之后,把"root error 之后 sibling terminal action"固化成 `store.py source-targets --rule terminal-action-error`。第一批 8 个候选中,`lightning preprocessEngine` 先退休为 `INVALID(parent-owner/branch-proof)`;随后 DFX `onFinished` 的局部 RED 被外层 `RunSubtask` 失败 cleanup owner 覆盖,`ImportSelectedRows` 和 `simplesst.flushSortedKVs` 也被 defer finalizer 证明覆盖,全部作为负样本入库。真正高质量命中是 `pkg/ingestor/ingestctrl/engine.go` 的 `sstIter.Close`:旧代码在 `i.iter.Close()` 返回错误时立即返回,跳过 `i.reader.Close()`。P:`sstIter` 同时 owns iterator 和 reader;Q:返回 iterator close root error 足以代表 Close 失败;F:reader 的底层 readable 没被 Close。RED 先用真实 pebble reader 观察到 `sstIter.Close` 后手动 `reader.Close()` 仍返回 nil,证明 reader 未关;随后 oracle 改成 `closeCountingReadable` 直接统计底层 readable close 次数。GREEN:本地最小修复改成先收集 `iterErr`,继续 `reader.Close`,最后 `multierr.Combine(iterErr, readerErr)`,同一测试 PASS。资产已入 `/Users/bba/pc/ai-native-assets/terminal-action-ingestctrl-sstiter-results.jsonl`;远端 `found_bug` 已入库为 id1260009 (`status=issue-filed,confirmed=1`,GitHub issue https://github.com/pingcap/tidb/issues/69757)。最新统计:asset_revisions=117,runs RED=18/GREEN=14/INVALID=2/INFO=1,targets validated=17/retired=13/blocked=1/candidate=3。方法论升级:terminal-action selector 必须在执行前补 `owner/finalizer dominance proof`;defer finalizer 和外层 cleanup owner 是高价值负样本,不能被源码形状误报带偏。

**2026-07-10aa ERROR_IDENTITY terminal-action lane 命中当前新 bug:**按上一轮推荐切到 `ERROR_IDENTITY_PRESERVATION` 后,没有复查 S3 multipart,而是从源码找"root error 之后还有 sibling terminal action"的证明义务。命中 `pkg/dxf/importinto/encode_and_sort_operator.go` 的 `chunkWorker.Close`:data writer `Close` 一旦返回错误,旧代码立即返回,跳过 `indexWriter.Close`。P:data/index writer 都已写入 buffered KV,且 data writer terminal `Close` 被注入根错误;Q:返回 data writer root error 足以代表 worker close failure;F:因为早返回,index writer 的 flush/close/onClose 全部跳过。RED:`TestAINativeChunkWorkerCloseClosesIndexWriterAfterDataCloseErrorRED` 在 current `13282a8` 的 vulnerable form 记录 `expected indexCloseCount=1 actual=0`,同时 root error `ai-native data writer close failed` 已返回;GREEN:本地最小修复改成记录 first/root error、继续 close index writer、最后返回 first error,同一测试 PASS 且日志出现 index writer `flush sorted kv`/`flush kv`/`close writer`。资产已入 `/Users/bba/pc/ai-native-assets/error-identity-importinto-chunkworker-close-results.jsonl`,RED/GREEN 证据在 `/Users/bba/pc/ai-native-assets/logs/error-identity-importinto-chunkworker-close-red.log` 和 `...-green.log`;远端 `found_bug` 已入库为 id1260008 (`status=issue-filed,confirmed=1`,GitHub issue https://github.com/pingcap/tidb/issues/69756)。最新统计:asset_revisions=112,runs RED=17/GREEN=13/INVALID=2/INFO=1,targets validated=16/retired=9/blocked=1,`store.py next` 返回 `null`。方法论升级:错误保真不能只问"最终 error 是不是 root error",还要问"系统在相信这个 error 已经表达失败之后,有没有跳过必须执行或必须禁止的 terminal action";这次 promoted 出 `scenario.multi-writer-terminal-close-after-peer-error.v1`。

**2026-07-10z state-ingress 动态队列已排空:控制面转入 selector 反哺与新规则阶段。**
`/Users/bba/pc/ai-native-assets/` SQLite 原型已把 selector/oracle/scenario/schedule/fault/obligation/module_profile/run_result/target_queue 做成可查询资产。五个 historical held-out 样本已 RED/GREEN 验证:`issue59055/fix59157`、`issue53843/fix53849`、`issue48164/fix48163`、`issue51846/fix52315`、`issue62424/fix62607`。随后完成一次 live-lift GREEN(`issue62424` on testbed 8220955)、三次 oracle-debt refill、一次 identity-token SOURCE_TARGETS 正负样本校准,并把 TiFlash MPP cache 候选退休为 `LOW_VALUE` 负缓存。最新主线是从 S23/id1230001 反推出 `STATE_INGRESS_INTERNAL_SQL` selector。第一正例 `target.source.binding-history-executeinternal-txreadts.v1` 已 RED/GREEN:current `13282a8` 上 `CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST` 消费 pending `tx_read_ts`,下一个 SELECT 从 stale rowset `[1]` 变成 current rowset `[1],[2]`;同一探针在临时 `ExecuteInternal` 隔离补丁下 GREEN。随后 `store.py source-targets --rule state-ingress` 生成 3 个候选:`ddl/foreign-key`、`executor/user-management`、`planner/index-advisor`。前两个经 target-analysis 退休:foreign-key 实际在 DDL worker/internal session 中执行,不是用户 session;user-management 走 sys session,也不能消费用户 pending `TxnReadTS`。第三个 index advisor 命中第二正例:`RECOMMEND INDEX RUN` 通过 `RecommendIndexExec.Next -> indexadvisor.AdviseIndexes -> indexadvisor.exec -> ExecuteInternal` 在当前 session 跑内部 SQL。RED 日志 `source-state-ingress-indexadvisor-txreadts-red.log` 记录 `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`;GREEN 日志 `source-state-ingress-indexadvisor-txreadts-local-green.log` 记录 `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]`。资产已入 `/Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl`。最新统计:asset_revisions=93,runs RED=14/GREEN=12/INVALID=2,targets validated=12/retired=4,queue_states validated=12/retired=4,`store.py next` 返回 `null`。方法论结论:这个 selector 已从单 bug 解释变成可复用资产;高效点是"源码证明义务 -> 小矩阵 -> 强 rowset oracle -> RED 后反推 selector -> source-targets 生成 -> target-analysis 负缓存/正例 GREEN"。本轮还修正了 fix-probe 经验:只在 `ExecuteInternal` 返回后恢复不够,因为 result drain/close 可能更晚触发 cleanup;有效隔离要在内部 SQL 入口前暂时移走用户 pending one-shot state,执行后再恢复。

**2026-07-10v 动态 source-targets 更新:**`store.py source-targets --rule state-ingress` 已从静态 seed 扩成动态扫描,并把 `session-ownership-proof` 固化成生成器门禁:已知 validated/retired path 去重;DDL worker、sys session、session pool、new session、nil restricted SQL、internal helper 会被筛掉或降权;同文件 sys/new-session marker 不再整文件一票否决,而是按局部 hit line 和 wrapper 信号打分。新候选已写入并导入 `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl`,共 9 个 target,初始全部停在 `needs_target_analysis`。第一目标 `target.source.dynamic-state-ingress.pkg-executor-show.v1` 已在 testbed 8220955 上 target-analysis:行为可复现,`SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts; SHOW TABLE STATUS LIKE 't'; SELECT ...` 后下一条 SELECT 读 current rowset `1,2`,而 direct `SET TRANSACTION; SELECT` 读 stale rowset `1`;证据在 `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-show-table-status-testbed8220955.log`。但 `SHOW TABLE STATUS` 本身是用户可见 SHOW/query statement,是否允许消费 SET TRANSACTION stale-read state 是产品合同问题,因此已入库为 `blocked/CONTRACT_NEEDED(show-is-next-query-statement)`,不是 bug claim。最新统计:targets blocked=1/candidate=8/validated=12/retired=4,queue_states blocked=1/needs_target_analysis=8/validated=12/retired=4;`store.py next` 返回 `target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1`。下一步继续对 infoschema/masking-policy load 做 session-ownership proof,不要直接 RED。

**2026-07-10w/x 动态队列继续推进并命中新 bug:**`infoschema` target 已退休为 `INVALID(sys-executor-factory-proof)`:masking-policy load 的 `UseCurSession` 用的是 `sysExecutorFactory` 创建的 internal/sys session,不是用户 statement session;资产在 `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-infoschema-retire-analysis.jsonl`。`BRIE` target 也退休为 `INVALID(new-glue-session-proof)`:外层是用户 `BACKUP/RESTORE`,但子任务 SQL 通过 `CreateSession`/`UseOneShotSession` 新建 glue session,不能消费用户 pending state;资产在 `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-brie-retire-analysis.jsonl`。随后队头 `check_table_index` 命中新 bug,但 bug 形状从 `STATE_INGRESS_INTERNAL_SQL` pivot 成 `USER_SESSION_STATE_RESTORE`: `FastCheckTableExec.Next` 把用户 session 的 `OptimizerUseInvisibleIndexes` 设为 true,defer 时硬重置为 false,没有恢复旧值。testbed 8220955 SQL-only RED:建 invisible index,`SET tidb_opt_use_invisible_indexes=ON; SET tidb_enable_fast_table_check=ON;` 前置 `EXPLAIN` 走 `IndexReader/IndexRangeScan`,执行 `ADMIN CHECK TABLE t` 后 `@@tidb_opt_use_invisible_indexes` 仍显示 1,但同一查询变成 `TableReader/TableFullScan`;fast-check-off 对照前后都保持 `IndexReader/IndexRangeScan`。证据日志在 `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955.log` 和 `...-fast-off-control.log`,资产已入 `/Users/bba/pc/ai-native-assets/source-state-ingress-check-table-index-results.jsonl`。最新统计:asset_revisions=100,runs RED=15/GREEN=12/INVALID=2/INFO=1,targets validated=13/retired=6/blocked=1/candidate=5,queue_states validated=13/retired=6/blocked=1/needs_target_analysis=5;`store.py next` 返回 `target.source.dynamic-state-ingress.pkg-executor-grant.v1`。方法论结论:source-target 的原标签可以只是入口,真正高效的是沿 P/Q/F 追到最近的可观测 state contract;`@@` 变量 oracle 太弱,必须补行为 oracle。

**2026-07-10y grant/revoke target-analysis 又命中新 bug:**`grant` target 先被证明不是 pending `TxnReadTS` ingress:`GrantExec` 主路径走 sys session,`userExists` 的 restricted SQL 没有 `UseCurSession`。但沿 session ownership 继续追,发现更准的 `SYS_SESSION_POOLED_STATE_ISOLATION` selector:`GrantExec` 会把 pooled sys session 的 `SessionVars.User` 设成 grantor,`ReleaseSysSession` 只 rollback/put 回池,不恢复 `User`;`RevokeExec` 重新拿 sys session 时不初始化 `User`,却把 `internalSession` 传给 `composeTablePrivUpdateForRevoke`,该 helper 用 `ctx.GetSessionVars().User.String()` 写 `mysql.tables_priv.Grantor`。testbed 8220955 SQL-only RED:用户 A 授予 `SELECT,INSERT` 后 `Grantor=ai_grantor_a@%`;用户 B 只撤销 `SELECT`,保留 `INSERT` 让行继续存在,结果 `Grantor` 变成空字符串,不是当前 actor B。证据日志 `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-grant-revoke-sys-session-user-testbed8220955.log`,资产 `/Users/bba/pc/ai-native-assets/source-state-ingress-grant-revoke-results.jsonl` 已把 grant/revoke 两个 target 都标为同一验证结果;bug 草案 `/Users/bba/pc/ai-native-grant-revoke-grantor-metadata-draft.md`。最新统计:asset_revisions=107,runs RED=16/GREEN=12/INVALID=2/INFO=1,targets validated=15/retired=6/blocked=1/candidate=3,queue_states validated=15/retired=6/blocked=1/needs_target_analysis=3;`store.py next` 返回 `target.source.dynamic-state-ingress.pkg-infoschema-issyncer-syncer.v1`。质量判断:这是 SQL 可见权限元数据 bug,不是权限绕过/安全攻击;方法价值是把"内部会话池状态不得流入用户可见 metadata"沉淀成可复用 selector,并证明 source-target 初始标签可以 pivot 到更准确的 state-owner oracle。

**2026-07-10z 队列排空与生成器反哺:**随后对剩余三个动态 target 做 target-analysis,没有直接执行 pending `TxnReadTS` oracle。`issyncer` 退休为 `INVALID(background-sys-session-pool-proof)`:MDL check 从 domain/background loop 借 `sysSessionPool`,没有用户 SQL wrapper。`importer/job` 退休为 `INVALID(task-manager-new-session-proof)`:IMPORT/SHOW IMPORT/Scheduler 都通过 DXF task-manager `WithNewSession/WithNewTxn` 访问 `mysql.tidb_import_jobs`。`importer/precheck` 退休为 `INVALID(new-session-precheck-proof)`:产品路径显式 `CreateSession(e.userSctx)` 后再做 precheck,避免用户 stale-read session 被复用。资产分别在 `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-issyncer-retire-analysis.jsonl` 与 `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-importer-retire-analysis.jsonl`。导入后统计:asset_revisions=107,runs RED=16/GREEN=12/INVALID=2/INFO=1,targets validated=15/retired=9/blocked=1,queue_states validated=15/retired=9/blocked=1;`store.py next` 返回 `null`。复跑 `source-targets --rule state-ingress`、`refill`、`source-targets --rule identity-token` 都没有新候选。控制面改进:把 grant/revoke、issyncer、importer job/precheck 写入 state-ingress 生成器负缓存/覆盖缓存,并新增两个窄规则:`store.py source-targets --rule pooled-session-state` 找"借 pooled sys session 后写 SessionVars 且未证明 restore"的路径,首次运行候选 0 且把 `pkg/executor/grant.go` 识别为已被 grant/revoke pivot 覆盖;`store.py source-targets --rule user-session-state-restore` 找"用户 session 变量临时打开后硬重置而非恢复旧值"的路径,首次运行候选 0 且把 `pkg/executor/check_table_index.go` 识别为已覆盖,把 `pkg/util/admin/admin.go` 归为 restores-original 绿样本。方法论结论:现有 state-ingress 搜索空间已闭环;下一步不是继续泛扫 executor,而是从已验证 selector 反推新的低噪声 SOURCE_TARGETS 规则,或切到另一个 oracle-debt/selector lane。

**2026-07-10 issue48164 S3 refill 发现当前新 bug:**这次从 historical issue48164 的 broad `oracle.injected-error-identity-survives.v1` 出发,没有重放旧 pipe bug,而是读当前 `pkg/objstore/s3store` 的 multipart writer 状态机。P:multipart upload 已开始且 part1 成功;part2 `UploadPart` 返回注入根错误;随后 cleanup/finalization 调 `Close`。旧 Q 被代码隐含成"Write 已返回错,Close 可以继续按已有 completeParts 完成";强 oracle 要求 terminal Close 不得 `CompleteMultipartUpload` 部分对象,并且要保留根错误。RED:`TestAINativeS3StorageCreateUploadPartFailureThenCloseRED` 在 current `13282a8` 上记录 `writeErr=ai-native mock upload part failed,closeErr=<nil>,completeCalls=1,completedParts=1`。GREEN:本地最小修复给 `multipartWriter` 增加失败状态,失败后 `Close` 走 `AbortMultipartUpload` 并返回原始错误,同一 storage 入口和底层 writer 入口均 PASS。资产已入 `/Users/bba/pc/ai-native-assets/issue48164-refill-s3-multipart-results.jsonl`;bug 草案在 `/Users/bba/pc/ai-native-s3-multipart-uploadpart-close-draft.md`;窄 oracle `oracle.s3-multipart-failed-part-no-complete-preserve-root.v1` 为 `execution_verified`;broad `oracle.injected-error-identity-survives.v1` 仍是 hypothesis。

**2026-07-10 issue51846 refill 发现当前 DDL owner epoch bug 候选:**这次从 historical issue51846 的 broad `oracle.allowed-state-after-topology-fault.v1` 出发,没有复读旧的 `runningJobs.processingIDs` 丢失 bug,而是读当前 `pkg/ddl/job_scheduler.go` 与 `pkg/ddl/reorg.go` 的 ownerTS 过滤逻辑。P:`runReorgJob` 在启动 reorg worker 前记录 `beOwnerTS`,worker 结束后只有 `res.ownerTS != curTS` 才认为是旧 owner 结果并走 retry。Q:ownerTS 相等就证明结果属于当前 owner epoch。F:`OnBecomeOwner` 用 `time.Now().Unix()` 设置 ownerTS,同一 TiDB 在一秒内 retire/re-become 时两个不同 owner epoch 可以共享 token。RED:`TestAINativeOwnerEpochSecondCollisionRED` 在 current `13282a8` 上记录 `previousOwnerTS=1000 curOwnerTS=1000`,说明旧 reorg result 会被当前 owner 接受。GREEN:本地最小修复增加 `renewOwnerTS(wallTS)`,当 wall-clock 未前进时用 `previous+1`,同一秒两次 owner epoch 得到不同 token,`TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult` PASS。资产已入 `/Users/bba/pc/ai-native-assets/issue51846-refill-owner-epoch-results.jsonl`;远端 `found_bug` 为 id1260006 (`status=issue-filed,confirmed=0`,GitHub issue https://github.com/pingcap/tidb/issues/69755);窄 oracle `oracle.ddl-stale-reorg-result-rejected-by-owner-epoch.v1` 为 `execution_verified`;broad `oracle.allowed-state-after-topology-fault.v1` 仍是 hypothesis。当前统计:asset_revisions=65,runs RED=9/GREEN=9,targets validated=9,queue_states validated=9。`store.py next` 返回 `null`;再次 `store.py refill` 没有新候选,四个 broad oracle debt 都因已有 refill target 被跳过。下一步应新增 `SOURCE_TARGETS` 规则或把这个 owner-epoch oracle lift 到 testbed,不要回到已完成 target。

**2026-07-10 SOURCE_TARGETS identity-token 结论:**队列清空后,从 DDL ownerTS bug 反推出新 selector `IDENTITY_TOKEN_ASYNC_FILTER`:代码 mint 一个 lifecycle/owner/session/heartbeat token,后面用 equality 接受或拒绝异步结果/状态迁移。第一轮不是粗扫 `time.Now().Unix()`,而是加了四个门:G1 token-gated decision,G2 lifecycle overlap,G3 product-feasible collision schedule,G4 strong state-action oracle。BR registry heartbeat 命中 G1/G2,但默认 heartbeat/stale-check 都是 60s,没有产品可行的 same-second stale 窗口,因此入库为 `INVALID(schedule-proof)` 负样本。随后 BR storewatch 命中 RED:现有 `updateStore` 只在 `StartTimestamp` 变化时触发 `OnReboot`;序列 `Up(T)->Offline(T)->Up(T)` 不会通知 reboot/recovery 用户。消费者上,BR backup 的 `OnReboot` 会触发 send-all retry policy,BR restore 的 `OnReboot` 会记录 reboot store 以 regenerate leaders。RED:`TestAINativeOnRebootWhenStoreRestartsWithinSameSecondRED` 失败于 `Should be true`;GREEN:本地最小修复保留 StartTimestamp 变化触发,并把 `non-Up -> Up` 也作为 conservative reboot/recovery notification,完整 `go test ./br/pkg/utils/storewatch` PASS。资产已入 `/Users/bba/pc/ai-native-assets/source-targets-identity-token-async-filter.jsonl` 和 `/Users/bba/pc/ai-native-assets/source-storewatch-reboot-same-second-results.jsonl`;草案在 `/Users/bba/pc/ai-native-br-storewatch-same-second-reboot-draft.md`。方法论价值:SOURCE_TARGETS 不只是造候选,还能产出"正例+负例"来校准 selector,防止以后把所有粗粒度时间戳都当 bug。

**2026-07-10p SOURCE_TARGETS 生成器固化:**新增 `python3 ai-native-assets/store.py source-targets --rule identity-token --repo /Users/bba/pc/tidb --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-identity-token-generated-20260710.jsonl`。本轮输出 1 个 target 并已入库:`target.source.tiflash-mpp-logical-core-starttimestamp.v1`。源码证据是 `pkg/planner/core/optimizer.go` 用 `GlobalMPPServerInfoManager` 的 cached `LogicalCPUCount` 和 `StartTimestamp` 判断是否刷新,而 `pkg/domain/infosync/tiflash_manager.go` 用 `tiflash.StartTime.Unix()` 暴露秒级 `StartTimestamp`。初步 target-analysis:G4 有可观察面,因为 `LogicalCPUCount` 会变成 fine-grained shuffle 的 `stream_count`,并写进 join/agg/window/exchange 物理计划字段;真正薄弱的是 G3,必须证明 TiFlash 同秒重启且 CPU 配置变化可产品化发生。当前只定性为候选,不是 bug;如果 G3 或效果证明失败,应记录为 `INVALID(schedule-proof/effect-proof)`。这轮真正验证的是资产复用机制:同一 selector 的旧正例/负例能被生成器去重,新源码形状能进入队列并被门禁停在 `needs_target_analysis`。

**2026-07-10q TiFlash MPP cache 候选退休:**对 `target.source.tiflash-mpp-logical-core-starttimestamp.v1` 做 target-analysis 后,已通过 `/Users/bba/pc/ai-native-assets/source-targets-tiflash-mpp-cache-retire-analysis.jsonl` 更新为 `retired/LOW_VALUE`。G1/G2 成立:相同 address + 相同 seconds-level `StartTimestamp` 会让 `LogicalCPUCount` cache 复用。G4 仅弱成立:`stream_count` 可观测但主要是计划/性能质量。G3 未证明:需要 TiFlash 同秒重启/重注册、地址复用、CPU 逻辑核变化三个条件同时成立;强行写等 timestamp 单测只会证明 token 精度,不能证明产品可行调度。生成器也已改进 skipped reason,现在会区分 `target_exists(status=validated)` 和 `retired_target_exists(status=retired)`;复跑后没有新 candidate,`store.py next` 为 `null`。随后 `store.py refill` 暴露一个控制面问题:会把 `oracle.identity-token-distinguishes-lifecycle.v1` 从已验证的 owner-epoch refill target 再次生成"refill 的 refill"。已修 `store.py refill`,禁止以 `target.refill.*`/`*-REFILL`/`source_kind=refill_candidate` 作为下一层 base;复跑 `refill-candidates-20260710-after-tiflash-retire.jsonl` 为 0 行,诊断为 `recursive_refill_base_only`。方法论价值:SOURCE_TARGETS 的收益不只是正例,还包括把低质量候选固化为负缓存;oracle-debt refill 也必须防止递归队列自我复制。

**2026-07-10s/t/u S23 state-ingress 增量完成两正两负:**从 id1230001 的经验抽象出 `STATE_INGRESS_INTERNAL_SQL`:外层代码清了/拒绝了一个 stale-read 入口,但内部 SQL 又走回通用 session 状态机,可能被 sibling session state 污染。本轮不是继续枚举 NT-DML,而是扫描 current-session internal SQL。第一正例是 binding-history:`CREATE BINDING FROM HISTORY` 通过 statement summary 查 plan digest 并调用 `ExecuteInternal`;稳定探针用 row1 `LastCommitTS + 10ms` 构造 `@stale_ts`,RED 记录 `before=467570589524557824 after=0 next_select_rows=[[1] [2]]`,GREEN 记录 `before=467570643908952064 after=467570643908952064 next_select_rows=[[1]]`。资产已入 `/Users/bba/pc/ai-native-assets/source-state-ingress-binding-history-tso-pair-results.jsonl`。随后 `store.py source-targets --rule state-ingress` 生成 3 个候选。`ddl/foreign-key` 被退休为 `INVALID(session-ownership-proof)`:其 restricted SQL 在 DDL worker session 中执行,不是用户 session。`executor/user-management` 被退休为 `INVALID(sys-session-isolation-proof)`:相关内部 SQL 走 sys session,同样不能消费用户 pending state。第二正例是 `planner/index-advisor`:源码锚点 `pkg/executor/recommend_index.go:75` 把当前 session 传给 `indexadvisor.AdviseIndexes`,`pkg/planner/indexadvisor/utils.go:533-549` 再用同一 session `ExecuteInternal` 并 drain 结果集。RED:`TestAINativeRecommendIndexPreservesPendingTxnReadTSRED` 在 current `13282a8` 记录 `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`;GREEN:临时补丁在内部 SQL 入口前隔离 pending `TxnReadTS`/`SnapshotInfoschema`,执行后恢复,同一探针记录 `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]` 并 PASS。资产已入 `/Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl`;临时源码补丁和探针均已删除。质量判断:两例都是用户可见 wrapper + 强 rowset oracle + local GREEN,方法论质量强;产品 bug 严重性仍取决于 `SET TRANSACTION READ ONLY AS OF TIMESTAMP` 是否只应被下一条用户读/execute 消费,而不是任意管理/helper statement。

**2026-07-10 issue53843 SQL-cancel refill 结论:**这次不是新增产品 bug,而是证明 oracle-debt refill 的方法有效。旧的 root-boundary RED 只证明 `UnregisterEngines` 会重复释放 memory ledger;新的 SQL-level RED 保留 SQL cancel、DDL 状态机和双 cleanup owner 由产品路径触发,只用 observing mock backend manager 暴露旧 `litBackendCtxMgr` 的非幂等 cleanup 语义。vulnerable 侧 `TestAINativeIssue53843SQLCancelDoubleCleanupRED` 失败于 exactly-once oracle:`registered=1,writes=1,unregister_calls=2,cleanup_ledger=-1`;current 侧 `TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource` GREEN:`active_writes=64,registered=1,finish_calls=1,live_engines=0,live_writers=0,duplicate_closes=0,disk_root_count=0`。方法论价值:从 broad oracle debt 出发,先压成 target-specific P/Q/F 和最小 RED/GREEN 矩阵,再用强 oracle 判断 scope;AI 可以改 TiDB/harness 做注入和日志,但必须记录 observer strength,防止 instrumentation mask 原 bug 或过度提升 broad oracle。

**2026-07-10 issue62424 RED/GREEN 结论:**这个 case 的关键不是泛泛"DDL inside txn",而是 GC 的 `ReportMinStartTS` 把 processlist 里的 `CurTxnStartTS` 当成活跃事务证明。DDL 入队前会隐式提交旧事务,但旧 startTS 仍可能在 DDL session 上可见;旧代码因此相信"有 CurTxnStartTS => GC 必须保护该时间戳"。fix PR 62607 在 `ReportMinStartTS` 中跳过 `StmtCtx.IsDDLJobInQueue` 的会话。upstream oracle `TestDDLInsideTXNNotBlockMinStartTS` 在 vulnerable `0501de48c5b033f17f300960ecfe4f40f9bc1742` 上 RED(`integration_test.go:279` 的 Eventually 条件一直不满足),在 fixed `e9e8a04fe71611ed08ebfcf0755993812a07c521` 上 GREEN(PASS)。已入库 `/Users/bba/pc/ai-native-assets/issue62424-analysis.jsonl` 和 `/Users/bba/pc/ai-native-assets/issue62424-results.jsonl`,新增窄 oracle `oracle.ddl-minstartts-ignores-queued-ddl.v1` 为 `execution_verified`;broad `oracle.no-stale-txn-state-after-ddl.v1` 仍是 hypothesis,因为完整 live-cluster GC safepoint 推进还未 lift。

**2026-07-10 issue62424 live-lift 结论:**用 testbed 8220955 验证了 root-boundary oracle 的集群可观测效果。构造 1048576 行表,事务内执行 `ADD INDEX`,DDL processlist 仍显示 `TxnStart=467568057103679489` 时,PD/etcd `/tidb/server/minstartts` 已推进到 `467568057116524554`,后续继续到 `467568066213183509` 和 `467568072111423519`。这说明当前 fixed testbed 上 queued DDL 的 stale TxnStart 没有 pin 住 server minStartTS。证据见 `/Users/bba/pc/ai-native-assets/logs/issue62424-live-gc-lift-green-testbed8220955-evidence.log` 和 `/Users/bba/pc/ai-native-assets/issue62424-live-lift-results.jsonl`。注意:GC safe point 本身仍受 10m cadence 影响,这次 lift 证明的是 server minStartTS,不是完整 GC safe point 周期。

**2026-07-10 issue51846 RED/GREEN 结论:**这个 case 的关键不是泛泛"PD leader partition",而是 DDL owner 退位后又成为 owner 时,旧 reorg worker 可能仍在运行。intro/fix 差异显示,旧代码在 `RetireOwnerHook` 中直接 `d.runningJobs = newRunningJobs()`,系统因此相信"已经不是 owner => 本地 processing 事实可丢弃";但 Q 不成立,因为已经 deliver 的 worker goroutine 还没退出。fix PR 52315 改成 `d.runningJobs.clear()`,只清 unfinished dependency maps,保留 processingIDs,避免同一个 ADD INDEX reorg job 被本机第二个 worker 重复调度。root-boundary oracle `TestAINativeOwnerRetirePreservesProcessingIDs` 在 vulnerable `bc841979a53e813d69c9fc8473ea0cc6703ef377` 上 RED(`Should be false`:retire 后同一 job 变 runnable),在 fixed `970962bdbc52547620be80817a7fc78e75b6221f` 上 GREEN(PASS)。已入库 `/Users/bba/pc/ai-native-assets/issue51846-analysis.jsonl` 和 `/Users/bba/pc/ai-native-assets/issue51846-results.jsonl`。

**2026-07-09 新 QA testbed `8220955` 已接入,本轮把目标重心明确为高质量缺陷:数据/索引不一致与常见查询 wrong-result。**
环境:namespace `testbed-tps-8220955-1-213`,TiDB `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`。
低噪声校准结果:① 通用 `USE INDEX` vs `IGNORE INDEX` 行集矩阵覆盖 prefix/collation、varchar-number coercion、unsigned boundary、generated/expression index 共 16 格,15 个 GREEN(triggered) + 1 个 INVALID(no index path,TableDual),`ADMIN CHECK` 均绿;② S7 getter/cache 校准里 `GROUP_CONCAT` × `group_concat_max_len` 在 prepared plan cache hit 后仍匹配 cache-disabled/current-session reference,是 GREEN;`default_week_format`/`div_precision_increment` 的本次参数化 scalar 形态未命中 plan cache,按 INVALID_NO_CACHE_HIT 处理;③ 重新执行 id30007 最小格,`REORGANIZE PARTITION p1 -> p1a/p1b` 后 `USE INDEX(idx_b)` 只返回 `12:120`,`IGNORE INDEX(idx_b)` 返回 `12:120,30:300`,`ADMIN CHECK TABLE` 报 8223,说明 S5 sibling-iterator/global-index 仍是当前 master 上的高质量 QA 红格。该结果已回写 `ai-native-ddl-methodology.md` 和 `ai-native-selector-ledger.md`;它是 selector 健康/跨版本持续性证据,不是新 root-cause 计数。补充校准:IndexMerge `a=1 OR b=1` 用 `USE_INDEX_MERGE(t,ia,ib)` 触发 union IndexMerge,与 `NO_INDEX_MERGE` 行集同为 `1,2,3,4,5`;带显式 binary collation 的 `a=1 OR c COLLATE utf8mb4_bin='A'` 没触发 IndexMerge,按 INVALID/非红格处理。下一 tick 不要宽泛随机 fuzz,优先继续寻找“源码全量访问/证明义务 + O2' 行集差分 + O1 ADMIN CHECK 双 oracle”的新 sibling iterator 或 index-proof 目标。

**2026-07-09 续跑一轮 LOOP tick:**按 STATE-VERIFY 只读确认后,从 `pkg/planner/core/indexmerge_unfinished_path.go` 选择 `IndexMerge OR path + top-level AND/residual filter preservation` 作为非重复 common-query wrong-result 候选。源码义务:IndexMerge 生成会把 OR branch 和 top-level AND 过滤条件拆进 partial paths,再通过 `KeepIndexMergeORSourceFilter`/`TableFilters` 保留原谓词。testbed `8220955` 上 4 格小矩阵全部 GREEN(triggered):top-level AND 进入 composite range、top-level residual expression、binary-collation residual、branch-local CNF 都触发 `IndexMerge`,并与 `NO_INDEX_MERGE()` reference 行集一致;`ADMIN CHECK TABLE` 绿。已把该 GREEN 校准写回 `ai-native-selector-ledger.md` 的 S22-candidate 和 `ai-native-oracle-library.md` 的 O2' extension。结论:当前不是新 root-cause;IndexMerge 不能泛化为高密度目标,只有当源码显示某类 residual/filter 可能被移除、弱化或参数敏感时才重开。

**2026-07-09 继续 LOOP tick:**从 `pkg/ddl/partition.go` 的 `DROP PARTITION` 状态机选择 final-state overlap/global-index reuse 义务。源码说明 StateWriteOnly 允许读映射到 higher-range/LIST DEFAULT overlapping partition,后续 StateDeleteReorganization 清理 dropped partition 的 global index entries。本轮不先打中间态 failpoint,而先验证 final-state 强 oracle:① RANGE drop p0 后,复用 dropped row 的 global-unique key `b=100` 插入 overlapping range;② LIST DEFAULT drop p1 后,复用 `b=10` 插入 DEFAULT。两格均 GREEN(triggered):`USE INDEX(idx_b)` 与 `IGNORE INDEX(idx_b)` 行集一致,RANGE 为 `6:100,7:101,12:120,30:300`,LIST DEFAULT 为 `1:10,2:20,3:30,9:90`,且 `ADMIN CHECK TABLE` 绿。已写回 S5。方法论边界:final-state drop-overlap 降权;只有能 hold 住 StateWriteOnly/StateDelete* 并证明中间态对用户可见行集/duplicate-key/ADMIN CHECK 异常时,才升级为 stateful target。

**2026-07-09 LOOP3 stateful feasibility:**当前 `tc-tidb` status NodePort `10080:31188` 的 `/fail/` 返回 404,不是可用 failpoint API;本机 18080/10080 也无 port-forward。于是尝试纯 SQL `ADMIN PAUSE DDL JOBS` 持有 `DROP PARTITION` global-index cleanup 中间态:临时 Go probe `/tmp/tidb-ddl-midstate-probe` 创建 60k/200k 行 dropped partition,把 DDL reorg worker/batch 临时调小并在退出时恢复。两次都观察到 job 进入 `delete reorganization`,但 pause 请求直到 job `synced` 才结束,最终 `row_count=60000/200000`;未取得 paused-state rowset,verdict=`INVALID(harness/env)`。已写回 S5。方法论改进:中间态 oracle 必须先证明 hold 点真实生效;没有 failpoint-enabled TiDB 或确定性 hold 点时,不得把 final-state GREEN 外推到 StateWriteOnly/StateDelete*。

**2026-07-09 LOOP4 MVI/IndexMerge calibration:**按 NO-REPLAY 跳过普通 IndexMerge 与 S5 final-state 后,转向 `pkg/planner/core/indexmerge_path.go` 的 MVI composed IndexMerge 义务。源码风险点包括 MV access-filter mutation、same MV index 多 partial path、MVI + normal index intersection、以及 parameter-sensitive JSON array predicates。testbed `8220955` 上最小矩阵为 GREEN(triggered)/INVALID(cache-shape) 而非新 root-cause:① `1 MEMBER OF(a) AND 2 MEMBER OF(b)` 双 MVI intersection 触发 `IndexMerge` 并与 `IGNORE_INDEX` 行集同为 `1,4,8`;② 加 `(d+0)=7` residual 时 EXPLAIN 显示 Probe `Selection`,行集仍 `1,4,8`;③ `JSON_CONTAINS(a,'[1,2]')` 单 MVI 多值 intersection 行集 `1`;④ `JSON_OVERLAPS(a,'[1,3]')` 单 MVI union 行集 `1,2,3,4,8`,fast/reference 一致。`ADMIN CHECK TABLE` 绿。prepared cache 校准: `? MEMBER OF(a)` 第二次执行命中 cache(`@@last_plan_from_cache=1`)且随参数返回当前 reference;`JSON_CONTAINS(a, ?)` 与 `JSON_OVERLAPS(a, ?)` 均未命中 prepared cache(`last_plan_from_cache=0`)但结果匹配 reference,所以只能作为 cache guard 校准/INVALID_NO_CACHE_HIT,不是 RED。方法边界:不要把 MVI IndexMerge 广泛 fuzz;下一步只重开源码显示 owner/type bit 丢失、array predicate 被错误移除、或 cache guard 失效的具体子形态。

**2026-07-09 id1230001 已入库为 confirmed,`BATCH` non-transactional DML 会继承 `SET TRANSACTION READ ONLY AS OF TIMESTAMP` 的 stale split range 并静默漏改当前行**。远端 `found_bug` 当前 `MAX(id)=1230001,COUNT=72,COUNT(DISTINCT root_cause_id)=50`,id1230001=`Non-transactional DML silently uses stale tx_read_ts split range and misses current rows` 已 confirmed 入库,`root_cause_id=ntdml-tx-read-ts-split-range-stale`。testbed `8220955` 上,`t_bug` 先插入 `1:10`,记录 `@ts=NOW(6)`,1.3s 后插入 `2:20`;`AS OF TIMESTAMP @ts` 控制只看到 `1:10`。普通 `UPDATE t SET b=b+100` 在 `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts` 后按预期报 `ERROR 1105 only support read-only statement during read-only staleness transactions`;无 `tx_read_ts` 的 `BATCH ON a LIMIT 1 UPDATE` 更新两行得到 `1:110,2:120`;但带 `tx_read_ts` 时 `BATCH` 返回 `number of jobs=1, all succeeded`,最终行集是 `1:110,2:20`。源码根因:`HandleNonTransactionalDML` 只清 `SessionVars.ReadStaleness`,但没有清/拒绝 `TxnReadTS`;`buildShardJobs` 通过 `se.Execute(selectSQL)` 跑 split-range SELECT,`staleReadProcessor.evaluateFromStmtTSOrSysVariable` 仍会消费 `TxnReadTS`,于是只枚举旧快照里的 range,后续 split DML 成功提交。草案 `/Users/bba/pc/ai-native-ntdml-tx-read-ts-stale-split-draft.md`,方法 case `/Users/bba/pc/ai-native-id1230001-method-case.md`。质量判断:高,这是普通用户可见的 silent partial write / wrong-rowset,不是 wrong-error;`ADMIN CHECK` 绿只能说明存储自洽,不能覆盖语义漏改。方法价值:新增 S23 `stale transaction input leak into split-range planning` 与 O29 `NTDML current-rowset oracle`;AI 提速来自把源码注释里的 "clear ReadStaleness" 扩展成完整 stale-read 输入清单,而不是枚举事务模式。暂停门:不要 fuzz 所有 BATCH 语法;只在另一个 stale input channel、DELETE/INSERT-SELECT 后果升级或 fix validation 时重开。

**2026-07-03 id1200002 已入库为 confirmed,`RELEASE SAVEPOINT` 会错误删除同一事务里更晚创建的 savepoint**。远端 `found_bug` 当前 `MAX(id)=1200002,COUNT=71,COUNT(DISTINCT root_cause_id)=49`,id1200002=`RELEASE SAVEPOINT deletes later savepoints, unlike MySQL savepoint semantics` 已 confirmed 入库,`root_cause_id=release-savepoint-drops-later-savepoints`。testbed `8192975` 上,事务内 `SAVEPOINT sp1; ... SAVEPOINT sp2; ... RELEASE SAVEPOINT sp1; ROLLBACK TO SAVEPOINT sp2` 返回 `ERROR 1305 SAVEPOINT sp2 does not exist`,且 `sp2` 后的写入仍留在事务内;按 MySQL 8.4 savepoint 语义,`RELEASE SAVEPOINT sp1` 只删除命名 savepoint,`ROLLBACK TO sp1` 才会删除后续 savepoint。源码根因在 `/Users/bba/pc/tidb/pkg/sessionctx/variable/session.go:529-535`: `ReleaseSavepoint` 注释和实现都按 rollback-like 语义做 `tc.Savepoints = tc.Savepoints[:i]`,把 `sp1` 和所有 later savepoints 一起删掉;`ROLLBACK TO` 在 `/Users/bba/pc/tidb/pkg/sessionctx/variable/session.go:541-548` 才是应该 truncate later savepoints 的操作。执行入口 `/Users/bba/pc/tidb/pkg/executor/simple.go:680-685` 直接调用该实现,已有测试 `/Users/bba/pc/tidb/pkg/executor/test/txn/txn_test.go:309-315` 和 `:443-445` 还把当前错误行为写成期望。草案 `/Users/bba/pc/ai-native-release-savepoint-stack-draft.md`,方法 case `/Users/bba/pc/ai-native-id1200002-method-case.md`。质量判断:中等,不是已提交数据损坏,但事务脚本里用户释放较早 savepoint 后无法回滚到较晚 savepoint,会造成可见 wrong-error 和事务内状态无法按用户预期恢复。方法价值:新增 S21 `txn stack operation semantic split` 与 O27 `savepoint_stack_semantics_oracle`;txn 模块的高效入口不是枚举隔离级别/语句组合,而是找“多个相邻操作共享一个状态栈,但语义效果不同”的代码,先列每个操作的契约,再用最小状态栈矩阵打红格。暂停门:不要枚举更多 savepoint 名字大小写或事务模式;只在另一个 txn state-stack 操作 split、更强后果、或 fix validation 时重开。

**2026-07-03 id1200001 已入库为 confirmed,`CREATE TABLE LIKE` 会把 source 的 `READ ONLY` table lock 复制到新表**。远端 `found_bug` 当前 `MAX(id)=1200001,COUNT=70,COUNT(DISTINCT root_cause_id)=48`,id1200001=`CREATE TABLE LIKE copies READ ONLY table lock to the new table` 已 confirmed 入库,`root_cause_id=create-like-copies-table-lock`。testbed `8192975` 上,非 partition 表 `src` 执行 `ALTER TABLE src READ ONLY` 后,`CREATE TABLE dst LIKE src` 成功,但 `INSERT INTO dst VALUES (2)` 报 `ERROR 8020 Table 'dst' was locked in READ ONLY ...`,错误携带与 `src` 相同的 lock session。控制格:只执行 `ALTER TABLE dst READ WRITE` 后,`INSERT INTO dst VALUES (3)` 成功,而 `src` 仍然 `READ ONLY`;只 cleanup `dst` lock 也能使 `dst` 可写且 `src` 保持只读。源码根因在 `/Users/bba/pc/tidb/pkg/ddl/create_table.go:1249-1298`: `BuildTableInfoWithLike` 先 `tblInfo := *referTblInfo`,显式重置 `ForeignKeys`、cache、TiFlash available、TTL、affinity 等字段,但没有重置 `TableInfo.Lock`;`ALTER TABLE ... READ ONLY` 在 `/Users/bba/pc/tidb/pkg/ddl/executor.go:1786-1803` 正是写入 `TableLockReadOnly`,后续 `/Users/bba/pc/tidb/pkg/ddl/table_lock.go:145-167` 信任该状态拒绝写入。草案 `/Users/bba/pc/ai-native-create-like-readonly-lock-draft.md`,方法 case `/Users/bba/pc/ai-native-id1200001-method-case.md`。质量判断:中等,不是数据损坏,但一次成功 DDL 造出用户未锁却不可写的新表。方法价值:把 S13 从 pointer-backed source mutation 扩展为 `target runtime-state clone`;最高效步骤是扫描浅拷贝后“哪些字段被 reset、哪些字段剩下且不是 schema definition”,再用目标行为 oracle 打红格。暂停门:不要枚举 `CREATE TABLE LIKE` option 或所有 `TableInfo` 字段;只在另一个未重置 runtime/non-schema 字段有行为 oracle、后果升级、或 fix validation 时重开。

**2026-07-03 id30040 已入库为 confirmed,`join_key_type_cast` 会让普通 INT-VARCHAR JOIN 漏掉科学计数字符串匹配行**。远端 `found_bug` 当前 `MAX(id)=1020001,COUNT=69,COUNT(DISTINCT root_cause_id)=47`,id30040=`join_key_type_cast can drop INT-VARCHAR join matches for scientific-notation strings` 已 confirmed 入库,`root_cause_id=join-key-type-cast-domain-narrowing`。testbed `8192975` 上,标量 contract 证明 `10='1e1'` 为真,`CAST('1e1' AS DOUBLE)=10`,但 `CAST('1e1' AS SIGNED)=1`,规则 guard `CAST(CAST(s AS SIGNED) AS DOUBLE)=CAST(s AS DOUBLE)` 为假。默认计划触发 `join_key_type_cast`,把原 DOUBLE 域 equality 改成 INT equality 并在 VARCHAR 侧插入 guard,结果 `ti.id=tv.s` 返回 `1:1,2:2e0,10:10,10:10.0`,漏掉 `10:1e1`;CASE-wrapped oracle 与禁用 `join_key_type_cast` 的 blacklist oracle 都返回 `1:1,2:2e0,10:10,10:10.0,10:1e1`。源码根因在 `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_join_key_type_cast.go`:规则只证明字符串能按 signed-int round-trip,却把原来的 mixed numeric/DOUBLE comparison 域替换成 INT 域;科学计数字符串是最小反例。草案 `/Users/bba/pc/ai-native-join-key-type-cast-scientific-notation-draft.md`,方法 case `/Users/bba/pc/ai-native-id30040-method-case.md`。质量判断:中等 wrong-result,普通 SELECT 静默漏行。方法价值:新增 S20 `semantic-domain rewrite narrowing` 与 O25 `join-domain reference`;这条证明“先从源码找 P/Q/F,列 D_dims,小矩阵打红格”的方法在非 DDL planner 里也有效。暂停门:不要枚举 numeric string 写法;只在另一个语义域替换、后果升级、或 fix validation 时重开。

**2026-07-03 id630025 已入库为 confirmed,`EXCHANGE PARTITION ... WITH VALIDATION` 在 LIST DEFAULT 分区上会因内部验证 SQL 错误而 wrong-error**。远端 `found_bug` 当前 `MAX(id)=1020001,COUNT=68,COUNT(DISTINCT root_cause_id)=46`,id630025=`EXCHANGE PARTITION WITH VALIDATION fails on LIST DEFAULT partitions due to invalid internal check SQL` 已 confirmed 入库,`root_cause_id=exchange-default-validation-sql`。testbed `8192975` 上,直接目标态证明 `INSERT INTO pt_direct VALUES (3)` 后 `SELECT ... PARTITION(pdef)` 可见 `3`,普通无 DEFAULT 的 LIST 分区 exchange validation 成功,但 `CREATE TABLE pt(a INT) PARTITION BY LIST(a) (...,PARTITION pdef DEFAULT); CREATE TABLE nt(a INT); INSERT INTO nt VALUES (3); ALTER TABLE pt EXCHANGE PARTITION pdef WITH TABLE nt` 返回 `ERROR 1064 ... near ") limit 1"`;同一合法行用 `WITHOUT VALIDATION` 可以交换成功。`LIST COLUMNS(... ) DEFAULT` 也有同形态错误。源码根因:`checkExchangePartitionRecordValidation` 调用 `buildCheckSQLConditionForListPartition`/`buildCheckSQLConditionForListColumnsPartition`,两个 builder 都只遍历当前 partition 的 `InValues` 且 TODO 明写 DEFAULT 未处理;DEFAULT partition 没有普通 `InValues`,于是生成近似 `not () limit 1` 的 restricted SQL。草案 `/Users/bba/pc/ai-native-exchange-partition-default-validation-draft.md`,方法 case `/Users/bba/pc/ai-native-id630025-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但不是 S15 静态 precheck 刷分;它来自 high-risk lane 的 validation safe path,新增 S19:`internal validation SQL builder must preserve omitted semantic dimensions`。暂停门:不要枚举 partition syntax;只在另一个 validation SQL builder 漏维度、wrong-acceptance/data-placement 后果或 fix validation 时重开。

**2026-07-03 id30039 已入库为 confirmed,`EXCHANGE PARTITION` 会把分区级 persisted ANALYZE options 泄漏给交换后的 standalone table**。远端 `found_bug` 当前 `MAX(id)=1020001,COUNT=67,COUNT(DISTINCT root_cause_id)=45`,id30039=`EXCHANGE PARTITION can leak saved ANALYZE options to the exchanged standalone table` 已 confirmed 入库,但 `root_cause_id` 复用 `exchange-idswap-orphan`,这是 blast-radius surface,不是新 root。testbed `8192975` 上,`ANALYZE TABLE pt PARTITION p0 COLUMNS a WITH 1 TOPN,3 BUCKETS` 后,`mysql.analyze_options` 在旧 `p0` physical ID 上保存 `column_choice=LIST,column_ids=1`;执行 `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION` 后,旧 `p0` ID 变成 `nt` 当前 table_id,该 options row 也被当前 `nt` 继承。随后 `ANALYZE TABLE nt WITH 2 TOPN,2 BUCKETS` 只分析列 `a` 和 `PRIMARY`,`mysql.stats_histograms` 中 `b/c` 仍为 `stats_ver=0`;无 exchange 的 standalone control 执行同一 `ANALYZE TABLE ... WITH` 会分析 `a/b/c/PRIMARY` 全部。源码根因:`onExchangeTablePartition` 只交换 `partDef.ID,nt.ID`,stats subscriber 只更新 global stats count/modify count,没有 remap/clear `mysql.analyze_options`;`AnalyzeExec.saveAnalyzeOptions` 继续按 `opts.PhyTableID` 保存/消费 options。草案 `/Users/bba/pc/ai-native-exchange-analyze-options-draft.md`,方法 case `/Users/bba/pc/ai-native-id30039-method-case.md`。方法价值:强化 O21/S4——side-row diff 只是 tier 1,必须再做 management/behavior round trip;本次 round trip 是 future `ANALYZE` 列选择被污染。暂停门:不要继续挖 stats/analyze_options 变体;只在另一个 owner 有新的行为 round trip、或修复验证时重开。

**2026-07-03 id30038 已入库为 confirmed,`ALTER TABLE ... ADD UNIQUE INDEX` 在 MVI 与多列 UNIQUE 同批添加且有并发 DML 时会 false duplicate**。远端 `found_bug` 当前 `MAX(id)=1020001,COUNT=66,COUNT(DISTINCT root_cause_id)=45`,id30038=`ADD UNIQUE INDEX can mis-detect duplicates for a multi-valued index when added with a multi-column index under concurrent DML` 已 confirmed 入库,`root_cause_id=addindex-mvi-key-owner-mismatch`。testbed `8192975` 上,关闭 dist task/fast reorg 后用 `mockBackfillSlow=return(true)` 只放大窗口;10 万行表同时 `ADD UNIQUE INDEX u_mvi((CAST(j AS SIGNED ARRAY))), ADD UNIQUE INDEX u_ab(a,b)`,在 write reorganization 期间并发 `UPDATE t SET b=b+7 WHERE a=90000`,DDL 稳定返回 `ERROR 1062 Duplicate entry '90000' for key 't.u_mvi'`,job rollback done 且表 `ADMIN CHECK` 通过。控制格:只加 `u_mvi` + 并发 JSON 更新成功;`u_mvi` + 一列 `u_b(b)` 成功;`u_mvi` + 两列 `u_ab(a,b)` + 并发 JSON 更新会卡在 `row_count=179998`,取消后返回 `invalid encoded key`。源码根因:`batchCheckUniqueKey` 先用 MVI `GenIndexKVIter` 把一个 `idxRecord` 展成多条 key,但后续 found-key duplicate classifier 用 flattened ordinal 的 `i%len(w.indexes)` 反推 owner;第二条 MVI key 被当成 sibling `u_ab(a,b)` 解码,把 same-row 已写 key 误判成重复/非法 key。草案 `/Users/bba/pc/ai-native-add-index-mvi-owner-mismatch-draft.md`,方法 case `/Users/bba/pc/ai-native-id30038-method-case.md`。质量判断:中等 DDL backfill wrong-error/online schema change liveness 风险,不是数据损坏;方法价值是新增 S1 refinement:`flattened generated artifact must carry owner/type bit`。暂停门:不要枚举 MVI 类型/数组元素;只在另一个 flattened-artifact owner/type bit 丢失、silent corruption、或 fix validation 时重开。

**2026-07-03 id630024 已入库为 confirmed,`EXCHANGE PARTITION` 允许 TTL 表与非 TTL 分区交换后留下 stale TTL status/timer side metadata**。入库后远端 `found_bug` 为 `MAX(id)=1020001,COUNT=65`,id630024=`EXCHANGE PARTITION leaves stale TTL status after swapping a TTL table ID` 已 confirmed 入库,但 severity 只记 low。testbed `8192975` 上,`nt` 为 standalone TTL 表且已跑过一次真实 TTL job,`pt` 为非 TTL 分区表;交换前 `nt table_id=16104`,`pt.p0 partition_id=16101`,`mysql.tidb_ttl_table_status` 有 `table_id=16104,parent_table_id=16104`。执行 `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION` 后,`nt table_id=16101`,`pt.p0 partition_id=16104`,但旧 TTL status 仍挂在 `16104`;timer sync 随后为新 `nt` 创建 `/tidb/ttl/physical_table/16101/16101`,并把旧 `/16104/16104` timer 置 `enable=0`,最终状态表可见两个 `nt` 历史/status ID。源码根因:`checkTableDefCompatible` 不比较 `TableInfo.TTLInfo`,`onExchangeTablePartition` 交换 `partDef.ID`/`nt.ID` 并处理 placement/TiFlash,但不 reconcile TTL status/timer rows。草案 `/Users/bba/pc/ai-native-exchange-partition-ttl-status-draft.md`,方法 case `/Users/bba/pc/ai-native-id630024-method-case.md`。质量判断:确认命中 S4,但只有 stale observability metadata,未证明 active wrong-delete/管理操作失效;方法价值是给 S4 加了两级 oracle:先证 storage-vs-current-owner diff,再用 cleanup/round-trip/active scheduling 给 severity 排序。暂停门:不要枚举 TTL options 或更多 system table;只在 TTL exchange 导致 active wrong scheduling/deletion、另一个 side-state owner 有 tier-2 行为失败、或 fix validation 时重开。

**2026-07-03 id630023 已入库为 confirmed,`MODIFY COLUMN` 会拒绝在无 NULL 数据的分区列上添加 `NOT NULL`**。入库时远端 `found_bug` 为 `MAX(id)=1020001,COUNT=64`,id630023=`MODIFY COLUMN rejects adding NOT NULL to partition columns with no NULL rows` 已 confirmed 入库。testbed `8192975` 上,直接目标态 `CREATE TABLE direct_range(a INT NOT NULL,b INT) PARTITION BY RANGE(a) ...` 可建并插入非 NULL 行;非分区表 `a INT NULL` 在无 NULL 行时 `ALTER TABLE ... MODIFY a INT NOT NULL` 成功,有 NULL 行时由通用数据检查报 `ERROR 1265`;但 `RANGE(a)`/`LIST COLUMNS(a)`/`KEY(a)`/`RANGE(TO_DAYS(datetime_col))` 分区表即使只有非 NULL 行也报 `ERROR 8200 Unsupported modify column: can't change the partitioning column...`。源码根因:`checkPartitionColumnModifiable` 在目标 partition definition validation 和通用 `checkForNullValue` 前先跑 `isAllowedPartitionColumnFlagChange`;该 helper 允许 `NOT NULL -> NULL`,但把 `NULL -> NOT NULL` 当作不安全 partition flag change 直接拒绝。草案 `/Users/bba/pc/ai-native-partition-column-not-null-draft.md`,方法 case `/Users/bba/pc/ai-native-id630023-method-case.md`。质量判断:中等 wrong-error,会阻塞“清理数据后给分区键加 NOT NULL”的 schema hardening;方法价值是把 S10 从 length/type metric mismatch 扩到 flag/nullability allowlist mismatch。边界校准:仓库已有测试显式期待当前 reject,但它没有 direct target/non-partition data-check oracle;本条记录的是目标态证明义务缺口。暂停门:不要枚举分区列 flag/type 变体;只在另一个 validation dimension、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id1020001 已入库为 confirmed,`CREATE USER IF NOT EXISTS` 在同一匿名账号已存在时仍会先校验 unused `PASSWORD EXPIRE` 而 wrong-error**。远端 `found_bug` 当前 `MAX(id)=1020001,COUNT=63`,id1020001=`CREATE USER IF NOT EXISTS can still fail while validating unused PASSWORD EXPIRE for anonymous user` 已 confirmed 入库。testbed `8192975` 上,已有 `''@'ai_s15_host'` 时,普通重复 `CREATE USER IF NOT EXISTS ''@'ai_s15_host'` 返回 `Note 3163 User ''@'ai_s15_host' already exists` 且 `mysql.user` 保持 `Password_expired=N,Account_locked=N`;但 `CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE` 返回 `ERROR 3016 The password for anonymous user cannot be expired`,即使该候选属性不会被使用。target-absent 控制格同样返回 `ERROR 3016` 且不创建账号。源码根因:`executor.SimpleExec.executeCreateUser` 先 `plOptions.loadOptions`,随后在 `userExists`/`IfNotExists` duplicate classifier 之前执行 `len(username)==0 && passwordExpired=="Y"` 校验,绕过 Note 3163 no-op。草案 `/Users/bba/pc/ai-native-create-user-if-not-exists-password-expire-draft.md`,方法 case `/Users/bba/pc/ai-native-id1020001-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但账号初始化脚本可见失败;方法价值是把 S15 从 `pkg/ddl` 对象 owner 迁移到账户 DDL,前提是先固定 same username+same host。负样本/防漂移:`ALTER SEQUENCE IF EXISTS` 与 `ALTER RESOURCE GROUP IF EXISTS` 源码上都是先检查目标存在再 validate options。暂停门:不要枚举 `CREATE USER` 选项;只在另一个 identity-pinned account DDL owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630022 已入库为 confirmed,`CREATE SPATIAL INDEX IF NOT EXISTS` 会在同名 index 已存在时仍先报 unsupported 而不是 duplicate no-op**。远端 `found_bug` 当前 `MAX(id)=630022,COUNT=62`,id630022=`CREATE SPATIAL INDEX IF NOT EXISTS can still fail before duplicate index no-op` 已 confirmed 入库。testbed `8192975` 上,已有 `idx_a ON t(a)` 时,普通重复 `CREATE INDEX IF NOT EXISTS idx_a ON t(b)` 返回 `Note 1061 Duplicate key name 'idx_a'` 且 `SHOW INDEX` 仍只有 `idx_a` on `a`;但 `CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)` 返回 `ERROR 8200 SPATIAL index is not supported`,即使同名 index 已存在且候选定义不会被使用。target-absent 控制格 `CREATE SPATIAL INDEX IF NOT EXISTS idx_sp_absent ON t(a)` 返回相同 `ERROR 8200` 且不创建 index。源码根因:`executor.createIndex` 在 `checkIndexNameAndColumns` duplicate classifier 之前先 `switch keyType`,遇到 `ast.IndexKeyTypeSpatial` 直接返回 unsupported,绕过 `ifNotExists` note path。草案 `/Users/bba/pc/ai-native-create-spatial-index-if-not-exists-draft.md`,方法 case `/Users/bba/pc/ai-native-id630022-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但 top-level index 幂等 DDL 可见失败;方法价值是把 S15 capability-gate-before-duplicate-classifier 迁移到常见 `CREATE INDEX` owner。负样本/防漂移:`CREATE DATABASE IF NOT EXISTS` 源码上是绿控,`CreateSchemaWithInfo` 先检查 schema exists,后做 charset/collation/placement validation。暂停门:不要枚举 index types/options;只在另一个 idempotent DDL owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630021 已入库为 confirmed,`CREATE MASKING POLICY IF NOT EXISTS` 会在目标 masking policy 已存在时仍校验未使用的候选表达式而 wrong-error**。远端 `found_bug` 当前 `MAX(id)=630021,COUNT=61`,id630021=`CREATE MASKING POLICY IF NOT EXISTS can still fail while validating unused masking expressions` 已 confirmed 入库。testbed `8192975` 上,已有 `p_mp ON t(a) AS a ENABLE` 时,合法重复 `CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a,'_x') DISABLE` 返回 `Note 1105 masking policy p_mp already exists` 且 `mysql.tidb_masking_policy` 保持 `expression=\`a\`,status=ENABLED`;但同一 policy/table/column 执行 `CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE` 返回 `ERROR 8275 masking policy expression can only reference the target column 'a'`,即使候选表达式不会被使用。target-absent 控制格 `CREATE MASKING POLICY IF NOT EXISTS p_absent ON t(a) AS b DISABLE` 返回相同 `ERROR 8275` 且不创建 policy。源码根因:`executor.CreateMaskingPolicy` 先解析表并调用 `buildMaskingPolicyInfo`,后设置 `OnExistIgnore` 并进入 `createMaskingPolicyWithInfo`;`buildMaskingPolicyInfo` 内 `validateMaskingPolicyExpression` 会在 duplicate classifier 前返回 `ErrMaskingPolicyExprInvalidColumn`。草案 `/Users/bba/pc/ai-native-create-masking-policy-if-not-exists-expression-draft.md`,方法 case `/Users/bba/pc/ai-native-id630021-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但 masking policy 初始化/迁移脚本可见失败;方法价值是把 id630020 的 builder/setter 抢跑进一步推广到 expression validator,且 owner 是安全/side-metadata DDL。暂停门:不要枚举 masking expression;只在另一个 idempotent DDL owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630020 已入库为 confirmed,`CREATE RESOURCE GROUP IF NOT EXISTS` 会在目标 resource group 已存在时仍构造并拒绝未使用的 `BACKGROUND` candidate option 而 wrong-error**。远端 `found_bug` 当前 `MAX(id)=630020,COUNT=60`,id630020=`CREATE RESOURCE GROUP IF NOT EXISTS can still fail while building unused BACKGROUND options` 已 confirmed 入库。testbed `8192975` 上,`tidb_enable_resource_control=ON`;已有 `ai_rg_s15 RU_PER_SEC=1000` 时,合法重复 `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000` 返回 `Note 8248 Resource group 'ai_rg_s15' already exists` 且 `SHOW CREATE RESOURCE GROUP` 保持 `RU_PER_SEC=1000`;但 `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=()` 返回 `ERROR 1105 unsupported operation. Currently, only the default resource group support change background settings`,即使候选定义不会被使用。target-absent 控制格 `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15_absent BACKGROUND=()` 返回相同 `ERROR 1105` 且不创建新 group。源码根因:`executor.AddResourceGroup` 先 `buildResourceGroup`,后检查 `ResourceGroupByName`;`buildResourceGroup` 内 `SetDirectResourceGroupSettings` 在 `ResourceGroupBackground` 分支拒绝非 default group,绕过后面的 `IF NOT EXISTS` Note 8248。草案 `/Users/bba/pc/ai-native-create-resource-group-if-not-exists-background-draft.md`,方法 case `/Users/bba/pc/ai-native-id630020-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但资源组初始化/迁移脚本可见失败;方法价值是证明 S15 create-like selector 不限于 table/sequence 的共享 `CreateTableWithInfo`,candidate builder 内部的 option gate 也要审计。负样本/防漂移:`CREATE VIEW IF NOT EXISTS` 语法不支持,不是 bug;`CREATE PLACEMENT POLICY IF NOT EXISTS` 的 obvious invalid-option 路径是绿控,因为 semantic validation 在 existence check 后。暂停门:不要枚举 resource-group options;只在另一个 idempotent DDL owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630019 已入库为 confirmed,`CREATE SEQUENCE IF NOT EXISTS` 会在目标 sequence 已存在时仍校验未使用的候选 options 而 wrong-error**。远端 `found_bug` 当前 `MAX(id)=630019,COUNT=59`,id630019=`CREATE SEQUENCE IF NOT EXISTS can still fail while validating unused sequence options` 已 confirmed 入库。testbed `8192975` 上,已有 `seq START WITH 10 INCREMENT BY 2 MAXVALUE 10000` 时,合法重复 `CREATE SEQUENCE IF NOT EXISTS seq START WITH 20 INCREMENT BY 5 MAXVALUE 20000` 返回 `Note 1050` 且 `SHOW CREATE SEQUENCE` 保持旧定义;但 `CREATE SEQUENCE IF NOT EXISTS seq INCREMENT 0` 和 `... MAXVALUE 1 START WITH 2` 返回 `ERROR 4136`,候选 table option `CHARSET=utf8` 返回 `ERROR 8227`,即使候选 sequence 定义不会被使用。new-sequence 控制格仍应报相同错误;失败后旧 `seq` 不变,`new_seq_bad*` 均未创建。源码根因:`executor.CreateSequence` 在设置 `OnExistIgnore` 并进入 `CreateTableWithInfo` 之前先 `buildSequenceInfo`,其中 `validateSequenceOptions`/table option check 会返回 `ErrSequenceInvalidData` 或 `ErrSequenceUnsupportedTableOption`;真正 target-exists `Note 1050` 在 `createTableWithInfoJob` 后面才发生。草案 `/Users/bba/pc/ai-native-create-sequence-if-not-exists-prevalidation-draft.md`,方法 case `/Users/bba/pc/ai-native-id630019-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但迁移幂等性可见失败;方法价值是把 id630018 的 create-like selector 从 table owner 迁移到 sequence owner,证明不是 CREATE TABLE 专属。暂停门:不要枚举 sequence option;只在另一个 create-like owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630018 已入库为 confirmed,`CREATE TABLE IF NOT EXISTS` 会在目标表已存在时仍校验未使用的新定义而 wrong-error**。入库时远端 `found_bug` 为 `MAX(id)=630018,COUNT=58`,id630018=`CREATE TABLE IF NOT EXISTS can still fail while validating unused table definitions` 已 confirmed 入库。testbed `8192975` 上,已有 `t(a)` 时,普通重复 `CREATE TABLE IF NOT EXISTS t(b BIGINT,c VARCHAR(60))` 与有效 `CREATE TABLE IF NOT EXISTS t LIKE src` 都返回 `Note 1050` 且 `t` 不变;但 `CREATE TABLE IF NOT EXISTS t LIKE missing_src` 返回 `ERROR 1146`,候选定义 `INDEX idx_b(b)` 返回 `ERROR 1072`,候选分区表达式 `PARTITION BY RANGE(b)` 返回 `ERROR 1054`,即使目标表已存在且候选定义不会被使用。new-table 控制格仍应报相同错误;重复列候选 `t(a INT,a INT)` 是绿格,说明不是所有无效定义都抢跑,而是部分 candidate/source validator 在 target-exists no-op 前执行。源码根因:`executor.CreateTable` 在设置 `OnExistIgnore` 并进入 `CreateTableWithInfo` 之前先解析 LIKE source、构建 TableInfo、跑 partition/index/FK 等候选校验;真正 `TableExists + OnExistIgnore -> Note 1050` 在 `createTableWithInfoJob` 后面才发生。草案 `/Users/bba/pc/ai-native-create-table-if-not-exists-prevalidation-draft.md`,方法 case `/Users/bba/pc/ai-native-id630018-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但迁移幂等性可见失败;方法价值是 S15 新增"target-exists after candidate validation"。暂停门:不要枚举 CREATE TABLE 选项;只在另一个 create-like owner、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630017 已入库为 confirmed,`DROP INDEX IF EXISTS \`PRIMARY\`` 在无主键表上仍会 wrong-error**。入库时远端 `found_bug` 为 `MAX(id)=630017,COUNT=57`,id630017=`DROP INDEX IF EXISTS PRIMARY still errors when no primary key exists` 已 confirmed 入库。testbed `8192975` 上,普通缺索引 `ALTER TABLE no_pk DROP INDEX IF EXISTS missing_i` 正确返回 `Note 1091 index missing_i doesn't exist`;但同一张无主键表执行 `ALTER TABLE no_pk DROP INDEX IF EXISTS \`PRIMARY\`` 或 `DROP INDEX IF EXISTS \`PRIMARY\` ON no_pk` 仍返回 `ERROR 1091 Can't DROP 'PRIMARY'; check that column/key exists`,且和不带 `IF EXISTS` 的错误相同。控制格:真实存在的 `PRIMARY`(`PRIMARY KEY NONCLUSTERED`) 带 `IF EXISTS` 可以正常 drop,普通缺索引无 flag 仍应 hard error。源码根因:`dropIndex` 在 `indexInfo == nil && ifExist` 的 missing-index note handler 之前先调用 `CheckIsDropPrimaryKey`;当 indexName 是 `PRIMARY` 且 `indexInfo=nil` 时 helper 直接返回 `ErrCantDropFieldOrKey`,绕过 safe path。草案 `/Users/bba/pc/ai-native-drop-index-primary-if-exists-draft.md`,方法 case `/Users/bba/pc/ai-native-id630017-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但迁移幂等性可见失败;方法价值是 S15 新增"special-name classifier before missing-object catch"。暂停门:不要枚举 DROP INDEX 拼写;只在同类 special-name/helper-before-existence-catch、silent duplicate-write/wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id30037 已入库为 confirmed,S7 getter-level scan 命中新的 `_utf8mb4` literal collation hidden input**。入库时远端 `found_bug` 为 `MAX(id)=630016,COUNT=56`,id30037=`Prepared plan cache reuses _utf8mb4 literal collation after default_collation_for_utf8mb4 changes` 已 confirmed 入库。testbed `8192975` 上,直接合同为 `default_collation_for_utf8mb4=utf8mb4_bin` 时 `COLLATION(_utf8mb4'A')=utf8mb4_bin` 且 `_utf8mb4'A'=_utf8mb4'a'` 为 `0`,切到 `utf8mb4_general_ci` 时 direct SQL 为 `utf8mb4_general_ci/1`;但 prepared statement 在 `bin` 下首次执行后,切到 `general_ci` 再执行命中 plan cache(`@@last_plan_from_cache=1`)仍返回 `utf8mb4_bin/0`。用户查询形态也复现:`WHERE _utf8mb4'A'=_utf8mb4'a'` 在 cache hit 下返回 `0`,同一 session direct SQL 返回 `2`;`ADMIN FLUSH SESSION PLAN_CACHE` 后同一 prepared statement 返回 `2`。显式 `COLLATE utf8mb4_general_ci` 控制格为绿。源码锚点:`expression_rewriter.go:adjustUTF8MB4Collation` 把 `GetDefaultCollationForUTF8MB4()` 写入 underscore-charset literal 的 field type;`default_collation_for_utf8mb4` 是 session/global 可改变量(会 warning 1681);prepared plan-cache key 只有 connection charset/collation,没有这个变量。草案 `/Users/bba/pc/ai-native-utf8mb4-default-collation-plan-cache-draft.md`,方法 case `/Users/bba/pc/ai-native-id30037-method-case.md`。方法价值:证明新的 getter-level 方法不是事后解释,而是可继续预测新 hidden input;不要因为 connection collation 已在 key 里就误判所有 collation 语义都 key-covered。
后续校准: `@user_var` 类型/collation direct 语义能变(`@u` bin->general_ci 让 equality 0->1),但 prepared statement 切换后 `@@last_plan_from_cache=0`,所以不是 stale-payload bug;`RAND()` 则相反,prepared plan cache 命中但连续值不同,说明 cache hit 没有冻结 volatile payload。S7 红格必须同时满足 direct drift、cache hit、old payload output、flush/current reference 四项。

**2026-07-03 id30036 已入库为 confirmed,S7 implicit-session-input 命中新的 aggregate/type-payload 子形态**。远端 `found_bug` 当前 `MAX(id)=630016,COUNT=55`,id30036=`Prepared plan cache reuses AVG decimal scale after div_precision_increment changes` 已 confirmed 入库。testbed `8192975` 上,直接合同为 `div_precision_increment=4` 时 `AVG(x)=1.5000`,切到 `8` 时 direct SQL 为 `1.50000000`;但 prepared statement `SELECT AVG(x),CAST(AVG(x) AS CHAR)` 在 precision 4 下首次执行后,切到 8 再执行命中 plan cache(`@@last_plan_from_cache=1`)仍返回旧 scale `1.5000`。用户查询形态也复现:derived table 里 `CAST(AVG(x) AS CHAR)='1.50000000'` 在 cache hit 下返回 `0`,同一 session direct SQL 返回 `1`;`ADMIN FLUSH SESSION PLAN_CACHE` 后同一 prepared statement 返回 `1`。源码锚点:`aggregation/base_func.go:typeInfer4Avg` 在构造聚合 descriptor 时读 `GetDivPrecisionIncrement` 并写入 `RetTp.Decimal`,`aggregation/avg.go:GetResult` 运行时虽然用当前 precision 做 decimal divide,但最终按 cached `RetTp.Decimal` round;prepared plan-cache key 没有 `div_precision_increment`。草案 `/Users/bba/pc/ai-native-avg-div-precision-plan-cache-draft.md`,方法 case `/Users/bba/pc/ai-native-id30036-method-case.md`,getter 扫描台账 `/Users/bba/pc/ai-native-s7-hidden-input-getter-scan.md`。质量判断:中等 wrong-result,与 id30035 区别是 table-data aggregate payload/类型元数据 stale,不是 all-constant scalar fold。方法价值:把 S7 的搜索单元升级为 `getter -> consumer -> cached payload class -> oracle`;下一步继续 getter 级扫描,优先找新的 payload class,不要枚举 AVG 参数类型。

**2026-07-03 id30035 已入库为 confirmed,S7 implicit-session-input constant-fold 命中第二个 hidden input**。远端 `found_bug` 当前 `MAX(id)=630016,COUNT=54`,id30035=`Prepared plan cache reuses division constants after div_precision_increment changes` 已 confirmed 入库。testbed `8192975` 上,标量合同为 `div_precision_increment=4` 时 `1/7=0.1429`,切到 `8` 时 direct SQL 为 `0.14285714`;但 prepared statement `SELECT 1/7` 在 precision 4 下首次执行后,切到 8 再执行命中 plan cache(`@@last_plan_from_cache=1`)仍返回旧值 `0.1429`。用户查询形态也复现:`WHERE CAST(1/7 AS CHAR)='0.142857142'` 在 cache hit 下返回 `0` 行,同一 session direct SQL 返回 `2` 行;`ADMIN FLUSH SESSION PLAN_CACHE` 后同一 prepared statement 返回 `2`。控制格:列值 `a/b` 在 cache hit 下跟随当前 precision。源码锚点:`builtin_arithmetic.go` 的 decimal division build/eval 都读 `GetDivPrecisionIncrement`,prepared plan-cache key 没有 `div_precision_increment`,constant folding 把全常量除法存成普通 `Constant`,现有 mutable-constant guard 只识别 `ParamMarker/DeferredExpr`。草案 `/Users/bba/pc/ai-native-div-precision-plan-cache-draft.md`,方法 case `/Users/bba/pc/ai-native-id30035-method-case.md`。质量判断:中等 wrong-result,prepared 查询会在 session 变量切换后投影旧值或漏行;方法价值是证明 id30034 的 selector 不是日期函数专属,下一步应做 `EvalContext`/`BuildContext` getter 级扫描,而不是枚举函数名。暂停门:不要枚举 decimal literal/除法写法/全部算术函数;只在另一个 hidden input family、不同 cached-payload mechanism 或 fix validation 时重开。

**2026-07-03 id30034 已入库为 confirmed,S7 新增 implicit-session-input constant-fold 子形态**。远端 `found_bug` 当前 `MAX(id)=630016,COUNT=53`,id30034=`Prepared plan cache reuses WEEK() constants after default_week_format changes` 已 confirmed 入库。testbed `8192975` 上,标量合同为 `default_week_format=0` 时 `WEEK('2008-02-20')=7`,`default_week_format=1` 时为 `8`;但 prepared statement 在 mode 0 下首次执行后,切到 mode 1 再执行命中 plan cache(`@@last_plan_from_cache=1`)仍返回旧值 `7`。用户查询形态也复现:`WHERE WEEK('2008-02-20')=8` 在 cache hit 下返回 `0` 行,同一 session direct SQL 返回 `2` 行;`ADMIN FLUSH SESSION PLAN_CACHE` 后同一 prepared statement 返回 `2`。控制格:列值 `WEEK(d)` 在 cache hit 下跟随当前 mode,显式 `WEEK(date,1)` 稳定正确,`YEARWEEK(date)` 单参数源码固定 mode 0 是边界样本。源码锚点:`builtinWeekWithoutModeSig.evalInt` 读 `GetDefaultWeekFormatMode`,prepared plan-cache key 没有 `default_week_format`,constant folding 把全常量 `WEEK('date')` 存成普通 `Constant`,现有 mutable-constant guard 只识别 `ParamMarker/DeferredExpr`。草案 `/Users/bba/pc/ai-native-week-default-week-format-plan-cache-draft.md`,方法 case `/Users/bba/pc/ai-native-id30034-method-case.md`。质量判断:中等 wrong-result,prepared 查询会在 session 变量切换后漏行;方法价值是把 S7 从"显式 key completeness"推进到"缓存 payload 还必须覆盖隐式 session/config 输入"。暂停门:不要枚举所有日期函数;只在另一个 hidden input 被 constant folding 变成 cached payload 或 fix validation 时重开。

**2026-07-03 id30033 已入库为 confirmed,S3 operator-semantic-arity 从 LIKE ESCAPE 迁移到 REGEXP_LIKE match_type**。远端 `found_bug` 当前 `MAX(id)=630016,COUNT=52`,id30033=`cluster_log REGEXP_LIKE ignores match_type and misses case-insensitive matches` 已 confirmed 入库。testbed `8192975` 上,`information_schema.cluster_log` 的 `MESSAGE` 是 `utf8mb4_bin`;标量合同格显示 `REGEXP_LIKE(_utf8mb4'gc_service.go' COLLATE utf8mb4_bin,'GC_SERVICE.GO','i')=1`,而 `match_type='c'` 为 `0`。同一 24h 窗口内,fast arm `REGEXP_LIKE(message,'GC_SERVICE.GO','i')` 返回 `0`,但等价 CASE scalar reference 返回 `35742` 行且 `self_true=35742`;控制格 `match_type='c'` 大写 pattern 为 `0/0`,小写 pattern 为 `35744/35744`。`EXPLAIN` 证明 fast arm 没有剩余 `Selection`,CASE arm 保留 `regexp_like(...,'i')` scalar Selection。源码锚点:`memtable_predicate_extractor.go` 的 `extractLikePattern` 把 `ast.RegexpLike` 当作二元 pattern extractor,只取 column+pattern 并移除原谓词,没有保留第三个 `match_type`。草案 `/Users/bba/pc/ai-native-clusterlog-regexp-like-match-type-draft.md`,方法 case `/Users/bba/pc/ai-native-id30033-method-case.md`。质量判断:中等 wrong-result,用户查询会漏掉大小写不敏感 regexp 可见日志;方法价值是证明"先枚举 scalar operator 的语义输入,再找 extractor 是否只建模了其中一部分"能从 LIKE `ESCAPE` 迁移到 REGEXP `match_type`。暂停门:不要枚举 regexp flag/pattern/同 helper 用户;只在不同 omitted semantic-input family、不同 replacement mechanism 或 fix validation 时重开。

**2026-07-03 id630016 已入库为 confirmed,`ADD PARTITION IF NOT EXISTS` 在 LIST DEFAULT 表上仍会 wrong-error**。远端 `found_bug` 当前 `MAX(id)=630016,COUNT=51`,id630016=`ADD PARTITION IF NOT EXISTS can still error on existing partition when LIST table has DEFAULT partition` 已 confirmed 入库。testbed `8192975` 上,没有 DEFAULT 的 LIST 表 `l_no_default(p0,p1)` 执行 `ALTER TABLE ... ADD PARTITION IF NOT EXISTS (PARTITION p0 VALUES IN (3))` 返回 `Note 1517 Duplicate partition name p0` 且 `p0,p1` 保留;但已有 DEFAULT 分区的 LIST 表 `l_default_dup(p0,pdef DEFAULT)` 执行同样重复 `p0` 的 IF NOT EXISTS ADD PARTITION,返回 `ERROR 8200 Unsupported ADD List partition, already contains DEFAULT partition. Please use REORGANIZE PARTITION instead`。控制格:DEFAULT 表新增 `p1` 仍应报 8200;无 DEFAULT 表新增 `p1` 正常成功。源码根因:`AddTablePartitions` 在 `executor.go:2300-2304` 先做 LIST DEFAULT capability gate 并返回 8200,后面 `checkPartitionDefinitionConstraints` 才会在 `ErrSameNamePartition && spec.IfNotExists` 时转 note。草案 `/Users/bba/pc/ai-native-add-partition-if-not-exists-default-precheck-draft.md`,方法 case `/Users/bba/pc/ai-native-id630016-method-case.md`。质量判断:低危 wrong-error,无数据损坏,但迁移幂等性可见失败;方法价值是 S15 新增"flag 存在、duplicate catch 存在,但更早 capability/default gate 在对象存在性分类前返回"。暂停门:不要枚举 partition syntax;只在同类 capability gate before existence classification 或 fix validation 时重开。

**2026-07-03 id630015 已入库为 confirmed,`DROP PARTITION IF EXISTS` 计数前置校验 wrong-error**。远端 `found_bug` 当前 `MAX(id)=630015,COUNT=50`,id630015=`DROP PARTITION IF EXISTS can still error when missing names look like all partitions` 已 confirmed 入库。testbed `8192975` 上,单分区表 `onep(p0)` 执行 `ALTER TABLE onep DROP PARTITION IF EXISTS px` 返回 `ERROR 1508 Cannot remove all partitions`,但 `px` 并不存在;两分区表 `twop(p0,p1)` 执行同样缺失名 `px` 则是预期的 `Note 1507` 且 `p0,p1` 保留。真实删除控制为绿:两分区删 `p0` 后只剩 `p1`;单分区删真实 `p0` 仍应报 1508。blast 形态:`twop DROP PARTITION IF EXISTS px,py` 和 `p0,px` 都报 1508,因为 requested-name count 达到 current partition count。源码根因:`CheckDropTablePartition` 先做 `len(oldDefs) <= len(partLowerNames)` 并返回 `ErrDropLastPartition`,再检查名字是否存在;executor 只会在后面把 `ErrDropPartitionNonExistent` 转成 note。现有测试只覆盖三分区删已存在 `p1`,没有覆盖缺失名/count boundary。草案 `/Users/bba/pc/ai-native-drop-partition-if-exists-count-precheck-draft.md`,方法 case `/Users/bba/pc/ai-native-id630015-method-case.md`。质量判断:低到中等 wrong-error,无数据错乱,但迁移幂等性可见失败;方法价值是 S15 新增"flag 存在但前置 aggregate precheck 用 raw requested names,在 existence catch 前返回别的错误"。暂停门:不要枚举 partition syntax;只在同类 raw-request precheck ordering gap 或 fix validation 时重开。

**2026-07-03 id30032 已入库为 confirmed,新增 S18 embedded constraint owner loss**。远端 `found_bug` 当前 `MAX(id)=630014,COUNT=49`,id30032=`ALTER TABLE ADD COLUMN silently drops inline CHECK constraints` 已 confirmed 入库。testbed `8192975` 上,`ALTER TABLE t ADD COLUMN b INT DEFAULT 1 CHECK (b > 0)` 成功且 `@@warning_count=0`,但 `SHOW CREATE TABLE` 只显示 `b int DEFAULT '1'`,`information_schema.check_constraints` 无对应 row,随后 `INSERT INTO t(a,b) VALUES (3,0)` 成功并得到 `3:0:0`。强参考:直接 `CREATE TABLE(a INT,b INT DEFAULT 1 CHECK(b>0))` 会发布 `t_create_inline_chk_1` 并拒绝 `b=0`(`ERROR 3819`);顺序 `ADD COLUMN b` 后 `ADD CONSTRAINT ck CHECK(b>0)` 也发布约束并拒绝 `b=0`;命名 inline column constraint 同样被静默丢弃。源码锚点:`CreateNewColumn` 调 `buildColumnAndConstraint` 时用 `col, _, err := ...` 丢掉 column-level CHECK constraints,`AddColumn` 只提交 `ActionAddColumn`,而 `CREATE TABLE` 和 table-level `ADD CHECK` 分别有真正的 CHECK metadata/remaining-row validation owner。草案 `/Users/bba/pc/ai-native-add-column-inline-check-loss-draft.md`,方法 case `/Users/bba/pc/ai-native-id30032-method-case.md`。质量判断:中等 schema-integrity bug,DDL 静默丢失用户请求的数据完整性约束;方法价值是把 S15 的"flag/spec 拆分丢失"升级成"嵌入式子义务归属丢失":父 owner 证明 column 可以加,不能证明 CHECK constraint 已发布。暂停门:不要枚举所有 column option;S18 只在不同 embedded sub-obligation owner、同根 fix validation 或更强后果 oracle 时重开。
后续边界校准:`pid INT REFERENCES p(id)` 在 direct CREATE 里也不发布 FK,不是 ALTER 专属红格;`ADD COLUMN b, ADD INDEX/CHECK/generated 使用 b` 这类同语句 target-schema 依赖虽会报错,但 TiDB 文档明确说明 multi-schema ALTER 按执行前 schema 校验,所以先记为 boundary/owner-ruling,不入库。

**2026-07-03 id30031 已入库为 confirmed,S3 operator-semantic-arity 做到第二个 extractor owner**。远端 `found_bug` 当前 `MAX(id)=630014,COUNT=48`,id30031=`information_schema LIKE with custom ESCAPE can return rows that fail the predicate` 已 confirmed 入库。testbed `8192975` 上,建表 `abc_def`、`abc#x`、`plain` 后,`information_schema.tables WHERE table_name LIKE '%#_%' ESCAPE '#'` fast arm 返回 `abc#x` 且投影自谓词 `self_true=0`;等价 CASE scalar reference 返回 `abc_def` 且 `self_true=1`;默认 escape 控制为绿:fast/reference 都返回 `abc#x:1`。源码锚点:`InfoSchemaBaseExtractor.Extract` 用 `extractLikePatternCol` 提取 pattern 并移除原谓词,`CompileLike2Regexp` 固定 default backslash escape,`filter` 用编译后的 regexp 过滤行。草案 `/Users/bba/pc/ai-native-infoschema-like-escape-draft.md`,方法 case `/Users/bba/pc/ai-native-id30031-method-case.md`。质量判断:中等 wrong-result,self-predicate 证据强;方法价值是 id30030 的 `LIKE ESCAPE` omitted input 已跨第二个 owner 复验。暂停门:这里必须停,不要枚举 `information_schema.columns/statistics/partitions` 等同 helper 用户;只在不同 omitted semantic input、不同 replacement mechanism 或 fix validation 时重开。

**2026-07-03 id30030 已入库为 confirmed,S3 新增 LIKE pattern-escape 语义维度**。远端 `found_bug` 当前 `MAX(id)=630014,COUNT=47`,id30030=`cluster_log LIKE with custom ESCAPE can drop matching log rows` 已 confirmed 入库。testbed `8192975` 上,`information_schema.cluster_log WHERE message LIKE '%#_%' ESCAPE '#'` 在 24h 时间窗 fast arm 返回 `0`,但等价 CASE scalar recheck 返回 `130683` 行且 `self_true=130683`;普通 SQL 合同控制证明 `'gc_service.go' LIKE '%#_%' ESCAPE '#' = 1`,而无 custom ESCAPE 的 `%#_%` 匹配 `abc#x`。默认 backslash escape 控制为绿:fast/reference 对 `message LIKE '%\_%'` 都返回 `130759`。源码锚点:`memtable_predicate_extractor.go` 的 LIKE extractor 只提取 pattern constant,`CompileLike2Regexp` 最终用 `CompilePattern(str,'\\')` 固定默认 escape,原 scalar predicate 被移出 `remained`。草案 `/Users/bba/pc/ai-native-clusterlog-like-escape-draft.md`,方法 case `/Users/bba/pc/ai-native-id30030-method-case.md`。质量判断:中等 wrong-result,用户查询会漏掉匹配日志;方法价值是把 S3 从"字符串/时间/缓存 key"继续精炼成"替代一个 scalar operator 前,必须枚举它的全部语义输入",LIKE 的 `ESCAPE` 就是隐藏输入。暂停门:不要枚举 cluster_log LIKE pattern;S3 只在不同 omitted operator input、不同 extractor owner/mechanism 或 fix validation 时重开。

**2026-07-03 id630014 已入库为 confirmed,S4 再命中一个 ID-swap side-state owner:masking policy**。远端 `found_bug` 当前 `MAX(id)=630014,COUNT=46`,id630014=`EXCHANGE PARTITION can orphan masking policies after table ID swap` 已 confirmed 入库,GitHub issue https://github.com/pingcap/tidb/issues/69754。testbed `8192975` 上,`CREATE MASKING POLICY mp_nt ON nt(a) AS a ENABLE` 后,交换前 `ALTER TABLE nt DISABLE/ENABLE MASKING POLICY mp_nt` 正常;执行 `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt` 后,`mysql.tidb_masking_policy` 仍显示 `table_name=nt`,但 `table_id` 等于 `pt.p0` 的 `tidb_partition_id`,不再等于当前 `nt` 的 `tidb_table_id`。用户层面 `ALTER TABLE nt DISABLE/DROP MASKING POLICY mp_nt` 和 `ALTER TABLE pt ...` 都报 `ERROR 1105 masking policy mp_nt doesn't exist`;重建同名 policy 只会在新 `nt` table_id 上新增一行,后续 DISABLE 只影响新行,旧 row 继续 `ENABLED`。源码锚点:`checkExchangePartition` 只拒绝 FK/affinity 等 table shape,`onExchangeTablePartition` 交换 `partDef.ID` 与 `nt.ID`,但没有类似 truncate 的 `updateMaskingPolicyTableIDAfterTruncate`。草案 `/Users/bba/pc/ai-native-exchange-partition-masking-policy-orphan-draft.md`,方法 case `/Users/bba/pc/ai-native-id630014-method-case.md`。质量判断:高质量 DDL side-state ownership bug,不是静态 sys table 差异,而是逻辑表管理命令已经无法触达旧策略;方法价值是证明"基础 owner 矩阵绿了以后,找改变同一 owner/key 却绕过 helper 的 sibling DDL entrypoint"有效。暂停门:不要继续扩 masking-policy 基础 rewrite/cleanup;S4 只在另一类 ID-swap/move-rekey owner、id630014 fix validation 或更强安全/数据行为 oracle 时重开。

**2026-07-03 id630013 已入库为 confirmed,新增 S17 DDL reorg 绕过 row invariant**。远端 `found_bug` 当前 `MAX(id)=630013,COUNT=45`,id630013=`MODIFY COLUMN can leave rows violating existing CHECK constraints` 已 confirmed 入库,GitHub issue https://github.com/pingcap/tidb/issues/69649。testbed `8192975` 上,`CREATE TABLE t(a DECIMAL(10,2), CHECK(a > 0)); INSERT (0.4),(1.2); ALTER TABLE t MODIFY a INT` 成功且 `SHOW WARNINGS` 为空,但最终 `SELECT a,a>0` 得到 `0,0` 与 `1,1`,`SHOW CREATE TABLE` 仍发布 `CHECK ((a > 0))`。同形态在 `VARCHAR('0.4')` 与 `DOUBLE(0.4)` 到 `INT` 上也复现。强 oracle:`CREATE TABLE ref(a INT); INSERT (0),(1); ALTER TABLE ref ADD CHECK(a > 0)` 按预期报 `ERROR 3819`,普通 `INSERT INTO t VALUES(0)` 也报 `ERROR 3819`;说明 CHECK 本身有效,是 MODIFY COLUMN data reorg 绕过了 post-conversion CHECK。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/column.go:754-815` 和 `:847-863`: `updateColumnWorker` cast 后直接 `txn.Set`;而普通 DML 在 `/Users/bba/pc/tidb/pkg/table/tables/tables.go:508-510`/`:888` 会调用 `CheckRowConstraint`,`ADD CHECK` 在 `/Users/bba/pc/tidb/pkg/ddl/constraint.go:354-389` 会扫描现有行。草案 `/Users/bba/pc/ai-native-check-constraint-modify-column-reorg-bypass-draft.md`,方法 case `/Users/bba/pc/ai-native-id630013-method-case.md`。质量判断:高质量数据完整性 bug,DDL 静默留下违反已发布 CHECK 的数据;方法价值是把"证明义务"从 schema validator 推进到 DDL reorg writer:任何直接写 KV 的 DDL backfill 都必须重新证明 row invariant。旁路发现 `CREATE TABLE LIKE` 改源表 CHECK 名是已知 id630005,本轮不重复入库。暂停门:不要枚举所有类型转换;S17 只在另一个 raw reorg writer / 另一个 row invariant owner / fix validation 时重开。

**2026-07-03 id630012 已入库为 confirmed,S16 从"校验顺序"细化到"coarse P 不能证明 rich Q"**。远端 `found_bug` 当前 `MAX(id)=630012,COUNT=44`,id630012=`MODIFY COLUMN can make FK child signedness incompatible with parent` 已 confirmed 入库。testbed `8192975` 上,直接创建 parent `INT` / child `INT UNSIGNED` 的 FK 会按预期报 `ERROR 3780`;valid signed/signed `ON UPDATE CASCADE` 控制把 parent/child 从 `1` 更新到 `-1` 成功。但先创建 signed/signed FK,再执行 `ALTER TABLE c MODIFY COLUMN a INT UNSIGNED` 会成功且无 warning,`SHOW CREATE TABLE` 显示 child `a int unsigned` 仍引用 signed parent `p(a)`。随后父表 `UPDATE p SET a=-1 WHERE a=1` 触发 cascade 时用户层面报 `ERROR 1264 Out of range value for column 'a'`,行保持不变;`DROP FOREIGN KEY` 后重新 `ADD` 同一 FK 又按目标态报 `ERROR 3780`。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:284-288` 与 `:301-304`: CREATE/ADD FK 检查 type+unsigned+charset+collation,但 MODIFY FK check 只用 type/flen/decimal 相等提前返回。草案 `/Users/bba/pc/ai-native-fk-signedness-modify-unsigned-draft.md`,方法 case `/Users/bba/pc/ai-native-id630012-method-case.md`。质量判断:中等 wrong-acceptance,DDL 接受 direct target-state validator 会拒绝的 FK schema,后续用户 DML fail-stop;方法价值是 S16 同一源码 predicate 又预测出 signedness 红格,同时 collation/PK NULL 负样本说明必须先做 later-validator coverage pass。暂停门:不要枚举 FK type/action;S16 只在缺失维度没有后置完整 target-state validator、静默坏后果或 fix validation 时重开。

**2026-07-03 id630011 已入库为 confirmed,新增 S16 DDL validator ordering gap**。远端 `found_bug` 插入后为 `MAX(id)=630011,COUNT=43`,id630011=`MODIFY COLUMN allows NOT NULL child column for foreign key SET NULL actions` 已 confirmed 入库。testbed `8192975` 上,直接创建 `pid INT NOT NULL` 且 `ON DELETE SET NULL`/`ON UPDATE SET NULL` 的 child FK 会按预期报 `ERROR 1830`;但先创建 nullable child FK,再执行 `ALTER TABLE c MODIFY COLUMN pid INT NOT NULL` 会成功且无 warning,`SHOW CREATE TABLE` 显示非法目标状态 `pid int NOT NULL` + `ON DELETE/UPDATE SET NULL`。随后父表 `DELETE`/`UPDATE` 触发 SET NULL 时用户层面报 `ERROR 1048 Column 'pid' cannot be null`,行保持不变。守卫格:`ON DELETE RESTRICT` 下 nullable->NOT NULL 成功,说明不是普通 FK MODIFY 全禁。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:301-304`,`/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1912`,`/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1924`,`/Users/bba/pc/tidb/pkg/ddl/modify_column.go:2318-2320`,`/Users/bba/pc/tidb/pkg/ddl/executor.go:5329-5330`: MODIFY FK check 在处理 `NOT NULL` column option 之前执行,且 type/flen/decimal 未变时提前返回;CREATE/ADD FK 路径本来有 SET NULL + NOT NULL 的 target-state 拒绝逻辑。草案 `/Users/bba/pc/ai-native-fk-set-null-modify-not-null-draft.md`,方法 case `/Users/bba/pc/ai-native-id630011-method-case.md`。质量判断:中等 wrong-acceptance,DDL 接受非法 schema,后续用户 DML fail-stop;方法价值是新增"validator 运行在未完成 target state 上"的 selector。暂停门:不要枚举 FK action/type pair;S16 只在另一类 validator-ordering gap、静默坏后果或 fix validation 时重开。

**2026-07-03 id630010 已入库为 confirmed,S15 新增 spec-level flag 在 table-element-list 拆分时丢失**。远端 `found_bug` 当前 `MAX(id)=630010,COUNT=42`,id630010=`ADD IF NOT EXISTS table-element list still errors on existing indexes/check constraints` 已 confirmed 入库。testbed `8192975` 上,`CREATE TABLE idx_outer(a INT); ALTER TABLE idx_outer ADD IF NOT EXISTS (KEY idx_a(a))` 首次成功,同一语句第二次执行却报 `ERROR 1061 Duplicate key name 'idx_a'`;`CREATE TABLE ck_outer(a INT); ALTER TABLE ck_outer ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK (a > 0))` 首次成功,第二次报 `ERROR 3822 Duplicate check constraint name 'ck_a'`。守卫格:`ALTER TABLE col_outer ADD IF NOT EXISTS (b INT)` 第二次成功并给 `Note 1060 Duplicate column name 'b'`;`ALTER TABLE idx_inner ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a))` 第二次成功并给 `Note 1061 Duplicate key name 'idx_a'`;index/check metadata count 均为 1,说明是 wrong-error 而不是 duplicate-write。源码锚点是 `/Users/bba/pc/tidb/pkg/parser/parser.y:2401-2418`,`/Users/bba/pc/tidb/pkg/ddl/executor.go:1637-1652`,`/Users/bba/pc/tidb/pkg/ddl/add_column.go:160-166`,`/Users/bba/pc/tidb/pkg/ddl/executor.go:1809-1825`: parser 把外层 `IfNotExists` 放在 parent `AlterTableSpec`,split 后 column path 读 `spec.IfNotExists`,但 constraint path 只读 `constr.IfNotExists` 或完全不读。草案 `/Users/bba/pc/ai-native-add-if-not-exists-table-element-list-draft.md`,方法 case `/Users/bba/pc/ai-native-id630010-method-case.md`。质量判断:低到中等 wrong-error,用户可见迁移幂等失败;方法价值是把 S15 从 executor sibling 漏参扩展到 parser/spec split/AST rewrite 的 flag ownership 审计。暂停门:不要枚举所有 table-element syntax;S15 只在不同 spec-splitting/AST-rewrite flag 丢失、silent duplicate-write/错放行,或 fix validation 时重开。

**2026-07-03 id630009 已入库为 confirmed,S11 命中第三个 dependency gate owner:partial-index condition**。远端 `found_bug` 当前 `MAX(id)=630009,COUNT=41`,id630009=`MODIFY COLUMN rejects metadata-only changes on columns used by partial-index conditions` 已 confirmed 入库。testbed `8192975` 上,直接目标 `CREATE TABLE direct_comment(a INT,b INT COMMENT 'new-comment',c INT,INDEX idx_a(a) WHERE b > 0)` 成功,插入数据后 `ADMIN CHECK TABLE` 通过,`WHERE b > 0 AND a >= 1` 走 partial index 返回 2 行;直接目标 `b INT DEFAULT 5` 也成功且默认插入得到 `b=5`。但已有表 `CREATE TABLE t_comment(a INT,b INT,c INT,INDEX idx_a(a) WHERE b > 0)` 执行 `ALTER TABLE t_comment MODIFY COLUMN b INT COMMENT 'new-comment'` 报 `ERROR 8272 Cannot drop, change or modify column 'b': it is referenced in partial index 'idx_a'`;`ALTER ... DEFAULT 5` 同样报 8272。守卫格:非 condition 列 `c` 改 COMMENT 成功;`DROP INDEX idx_a` 后再改 `b` COMMENT 成功。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/executor.go:7565-7571` 与 `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:315`/`:1855`: `checkColumnReferencedByPartialCondition` 只证明列出现在 `idx.AffectColumn`,但 common MODIFY path 把 dependency existence 当成任何 MODIFY 都不安全。草案 `/Users/bba/pc/ai-native-partial-index-metadata-modify-draft.md`,方法 case `/Users/bba/pc/ai-native-id630009-method-case.md`。质量判断:低到中等 wrong-error,用户可见 schema evolution 被阻塞;方法价值是 S11 从同一 generated-column gate 扩展到另一个独立 dependency gate。暂停门:不要枚举 partial-index predicate syntax;S11 只在 silent wrong-acceptance、不同 checker owner 或 fix validation 时重开。

**2026-07-03 id630008 已入库为 confirmed, 新增 S15 DDL idempotence flag dropped**。远端 `found_bug` 当前 `MAX(id)=630008,COUNT=40`,id630008=`ADD FOREIGN KEY IF NOT EXISTS still errors on existing foreign key` 已 confirmed 入库。testbed `8192975` 上,`ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS (pid) REFERENCES p(id)` 首次成功,`information_schema.referential_constraints` 只有一条 `fk_pid -> p`;同一语句第二次执行却报 `ERROR 1826 Duplicate foreign key constraint name 'fk_pid'`。守卫格:普通重复 `ADD FOREIGN KEY` 也报 1826,说明硬错误路径本身正常;兄弟路径 `ALTER TABLE idx_t ADD INDEX IF NOT EXISTS idx_a(a)` 成功并给 `Note 1061 Duplicate key name 'idx_a'`;`DROP FOREIGN KEY IF EXISTS` 当前语法不接受,不纳入此 bug。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/executor.go:1810-1818` 与 `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:644-648`: ADD INDEX/COLUMNAR 会传 `constr.IfNotExists`,但 ADD FOREIGN KEY 分支注释说明忽略该标志并直接 `CreateForeignKey`,随后 `checkFKDupName` 返回 `ErrFkDupName`。草案 `/Users/bba/pc/ai-native-fk-if-not-exists-draft.md`,方法 case `/Users/bba/pc/ai-native-id630008-method-case.md`。质量判断:低到中等 wrong-error,用户可见迁移幂等失败,无数据损坏;方法价值是新增 "parser/AST flag 在 sibling DDL 分支丢失" 选择器。暂停门:不要枚举 FK option;S15 只在另一个 DDL idempotence flag owner、silent duplicate-write/错放行,或 fix validation 时重开。

**2026-07-03 id630007 已入库为 confirmed, S11 跨第二个 dependency owner 命中 expression index companion case**。远端 `found_bug` 插入 id630007 时为 `MAX(id)=630007,COUNT=39`,id630007=`MODIFY COLUMN rejects metadata-only changes on columns used by expression indexes` 已 confirmed 入库。testbed `8192975` 上,直接目标 `CREATE TABLE direct_comment(a INT COMMENT 'new-comment', INDEX idx_expr ((a + 1)))` 成功,插入/查询 `a+1=2` 正常,`ADMIN CHECK TABLE` 通过;直接目标 `a INT DEFAULT 5, INDEX idx_expr ((a+1))` 也成功且默认插入得到 `5,6`。但已有表 `CREATE TABLE t_comment(a INT, INDEX idx_expr ((a + 1)))` 执行 `ALTER TABLE t_comment MODIFY COLUMN a INT COMMENT 'new-comment'` 报 `ERROR 3106` 包裹 `[ddl:3837]Column 'a' has an expression index dependency and cannot be dropped or renamed`;`ALTER ... DEFAULT 5` 同样报错。守卫格:非依赖列 COMMENT 成功,DROP INDEX 后再改 COMMENT 成功,真正 `a INT -> BIGINT` 仍拒绝。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1415-1427` 与 `:1863-1987`: 表达式索引被 hidden generated column 表示,`checkModifyColumnWithGeneratedColumnsConstraint` 对 hidden column 返回 `ErrDependentByFunctionalIndex`,rename path 精确使用它,但 common modify path 无条件把 dependency 当成所有 MODIFY 不安全。草案 `/Users/bba/pc/ai-native-functional-index-metadata-modify-draft.md`,方法 case `/Users/bba/pc/ai-native-id630007-method-case.md`。质量判断:中等 wrong-error,用户可见,强 oracle 是 direct target expression-index schema + `ADMIN CHECK`;但这是 id630004 的 companion/blast-radius case,不是新 root-cause family。暂停门:不要枚举 expression-index 语法;S11 只在另一个 dependency owner、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630006 已入库为 confirmed, 新增 S14 DDL recovery namespace validation bypass**。远端 `found_bug` 插入 id630006 时为 `MAX(id)=630006,COUNT=38`,id630006=`FLASHBACK TABLE can restore duplicate CHECK constraint names in one schema` 已 confirmed 入库。testbed `8192975` 上,`CREATE TABLE f(a INT, CHECK(a > 0)); DROP TABLE f; CREATE TABLE f(a INT, CHECK(a > 1)); FLASHBACK TABLE f TO f_old` 成功后,`SHOW CREATE TABLE f` 和 `SHOW CREATE TABLE f_old` 都显示 `CONSTRAINT f_chk_1`,且 `information_schema.check_constraints` 在同一 schema 下列出两行 `f_chk_1`、表达式分别为 `a > 0` 与 `a > 1`;两个表的违例插入也都报 `Check constraint 'f_chk_1' is violated`。守卫格:普通 `CREATE TABLE`/`ADD CHECK` 对同 schema 重名 CHECK 报 3822,`CREATE TABLE like_copy LIKE f` 会生成独立 `like_copy_chk_1`。源码锚点是 `/Users/bba/pc/tidb/pkg/executor/ddl.go:605-637` 与 `/Users/bba/pc/tidb/pkg/ddl/table.go:159-198`: `executeFlashbackTable` 只改 `TableInfo.Name`,`onRecoverTable` 只检查 table name/table ID,没有像 `/Users/bba/pc/tidb/pkg/ddl/create_table.go:73` 和 `/Users/bba/pc/tidb/pkg/ddl/constraint.go:153` 那样调用 `checkConstraintNamesNotExists`。草案 `/Users/bba/pc/ai-native-flashback-check-duplicate-draft.md`,方法 case `/Users/bba/pc/ai-native-id630006-method-case.md`。质量判断:中等质量 metadata-corruption,用户可见,强 oracle 是 schema-level CHECK namespace uniqueness。暂停门:不要枚举所有 recover 字段;S14 只在另一个 create/add 有 validator 但 recovery 绕过的 schema-level namespace/reference owner、行为级后果或 fix validation 时重开。

**2026-07-03 id630005 已入库为 confirmed, 新增 S13 DDL shallow-copy target mutation**。远端 `found_bug` 插入 id630005 时为 `MAX(id)=630005,COUNT=37`,id630005=`CREATE TABLE LIKE mutates source CHECK constraint names in SHOW CREATE TABLE` 已 confirmed 入库。testbed `8192975` 上,`CREATE TABLE src_auto(a INT, CHECK (a > 0))` 初始 `SHOW CREATE` 为 `CONSTRAINT src_auto_chk_1`;执行 `CREATE TABLE dst_auto LIKE src_auto` 后,`SHOW CREATE TABLE src_auto` 和新连接里的同一查询都显示源表约束名被改成 `dst_auto_chk_1`,非法插入 `src_auto(-1)` 报错也引用 `dst_auto_chk_1`;但 `information_schema.check_constraints` 仍列出 `src_auto_chk_1` 与 `dst_auto_chk_1`,元数据表面不一致。direct sibling controls `d1/d2` 各自得到 `d1_chk_1/d2_chk_1`,不互相污染。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/create_table.go:1249` 与 `:1298-1307`: `BuildTableInfoWithLike` 对 `referTblInfo` 做浅拷贝,随后 `renameCheckConstraint` 原地修改共享的 `*ConstraintInfo`。草案 `/Users/bba/pc/ai-native-create-like-check-source-mutation-draft.md`,方法 case `/Users/bba/pc/ai-native-id630005-method-case.md`。质量判断:中等质量 metadata-corruption,用户可见,强 oracle 是 source/target metadata isolation。暂停门:不要枚举所有 LIKE option;S13 只在另一个 pointer-backed metadata owner、行为级 source mutation 或 fix validation 时重开。

**2026-07-03 id630004 已入库为 confirmed, 新增 S11 DDL dependency gate overbroad**。远端 `found_bug` 当前 `MAX(id)=630004,COUNT=36`,id630004=`MODIFY COLUMN rejects metadata-only changes on columns used by generated columns` 已 confirmed 入库。testbed `8192975` 上, direct `CREATE TABLE direct_comment(a int COMMENT 'new-comment', b int GENERATED ALWAYS AS (a+1) STORED)` 成功且插入 `a=1` 得到 `b=2`;direct `a int DEFAULT 5` target 也成功且默认插入得到 `a=5,b=6`。但已有表 `a int, b as (a+1)` 执行 `ALTER TABLE ... MODIFY COLUMN a int COMMENT 'new-comment'` 或 `... DEFAULT 5` 都报 `ERROR 3106/3108`,schema 不变。守卫格:非依赖列 comment ALTER 成功,generated column 自己 comment ALTER 成功,真正改被依赖 base column type 仍拒绝。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1415` 和 `:1983`: `checkModifyColumnWithGeneratedColumnsConstraint` 只证明 dependency exists,rename path 精确使用它,但 common modify path 无条件把 dependency 当成所有 MODIFY 不安全。草案 `/Users/bba/pc/ai-native-generated-column-metadata-modify-draft.md`,方法 case `/Users/bba/pc/ai-native-id630004-method-case.md`。质量判断:中等质量 wrong-error,用户可见,强 oracle 是 direct target schema + generated value behavior reference。暂停门:不要枚举 generated expression syntax;S11 只在另一个 dependency owner、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630003 已入库为 confirmed, S10 命中新 DDL validator owner(partition-column modify)**。远端 `found_bug` 在插入 id630003 后为 `MAX(id)=630003,COUNT=35`,id630003=`MODIFY COLUMN rejects safe VARCHAR shrink on partition columns` 已 confirmed 入库。testbed `8192975` 上,TiDB 能直接创建并写入 `varchar(5)` 的 LIST/RANGE/KEY partition target schema;非分区表 `varchar(6)->varchar(5)` 在现有值 `MAX(CHAR_LENGTH)=3` 时也能 ALTER 成功;partition column `varchar(6)->varchar(7)` 成功。但 LIST/RANGE/KEY partition column 在 literals/data 都 fit 的 `varchar(6)->varchar(5)` 上均报 `ERROR 8200`,schema 保持 `varchar(6)`。KEY guard 显示 direct `varchar(6)` 与 `varchar(5)` KEY partition 表对采样值 `a,bb,ccc,dddd,中,中中` 的 partition membership 一致。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1559`: `checkPartitionColumnTypeChangeAllowlist` 对 string partition column 只允许 `isStringLengthExtension(newFlen > oldFlen)`,在 target partition-definition validation 和 generic data-fit check 前拒绝 safe shrink。草案 `/Users/bba/pc/ai-native-partition-column-varchar-shrink-draft.md`,方法 case `/Users/bba/pc/ai-native-id630003-method-case.md`。质量判断:中等质量 wrong-error,用户可见,强 oracle 是 direct target partition schema + non-partition data-fit reference。暂停门:不要枚举所有 partition/string 变体;S10 只在不同 validation metric、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630002 已入库为 confirmed, S10 泛化为 DDL validation metric mismatch**。远端 `found_bug` 在插入 id630002 后为 `MAX(id)=630002,COUNT=34`,id630002=`MODIFY COLUMN rejects foreign-key VARCHAR length changes that target schema accepts` 已 confirmed 入库。testbed `8192975` 上,TiDB 直接接受 parent `varchar(10)` / child `varchar(10 or 15)` FK,也接受 parent `varchar(15)` / child `varchar(20)` FK;但从已有 FK 表执行 child `varchar(20)->varchar(10/15)` 会报 `ERROR 1832`,parent `varchar(10)->varchar(15)` 且 child `varchar(20)` 会报 `ERROR 1833`。守卫格:child `20->25` 成功,parent `10->20` 成功。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:356`: `isAcceptableForeignKeyColumnChange` 对非 integer FK 修改强制 `newFlen >= relatedFlen` 且 `newFlen >= originalFlen`,比 `CREATE TABLE`/`ADD FOREIGN KEY` 的目标结构兼容性更严格。草案 `/Users/bba/pc/ai-native-fk-modify-column-length-draft.md`,方法 case `/Users/bba/pc/ai-native-id630002-method-case.md`。质量判断:中等质量 wrong-error,用户可见,强 oracle 是 direct target-schema acceptance reference。暂停门:不要枚举所有 FK type pair;S10 只在另一个 validation metric mismatch、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id630001 已入库为 confirmed, DDL 新增 S10 precheck metric mismatch**。远端 `found_bug` 在插入 id630001 后为 `MAX(id)=630001,COUNT=33`,id630001=`MODIFY COLUMN shrink rejects valid multibyte CHAR/VARCHAR values` 已 confirmed 入库。testbed `8192975` 上,`varchar(3)` 和 `char(3)` 目标表都能直接插入 `_utf8mb4'中中中'`(`LENGTH=9,CHAR_LENGTH=3`);ASCII 控制 `varchar(4)->varchar(3)` 成功;但 `varchar(4)->varchar(3)` 和 `char(4)->char(3)` 对同一个 `中中中` 值执行 `ALTER TABLE ... MODIFY COLUMN` 均报 `ERROR 1265 Data truncated`,且 `SHOW CREATE TABLE` 仍停在旧长度。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:879`: no-reorg-with-check 路径用 `LENGTH(col) > newFlen` 做范围预检查,把字节长度当成了非 binary `CHAR/VARCHAR` 的字符长度契约。草案 `/Users/bba/pc/ai-native-modify-column-multibyte-shrink-draft.md`,方法 case `/Users/bba/pc/ai-native-id630001-method-case.md`。质量判断:中等质量 wrong-error,用户可见,强 oracle 是 target-type direct acceptance reference。暂停门:不要枚举所有 charset/string 类型;S10 只在另一个 precheck metric mismatch、silent wrong-acceptance 或 fix validation 时重开。

**2026-07-03 id600001 已入库为 confirmed, 回到 DDL 后新增 S9 identity proof fast path**。远端 `found_bug` 在插入 id600001 后为 `MAX(id)=600001,COUNT=32`,id600001=`REORGANIZE PARTITION silently drops duplicate nonclustered rows after EXCHANGE PARTITION` 已 confirmed 入库。testbed `8192975` 上,普通 `REORGANIZE PARTITION` 计数 2->2;但先用 `EXCHANGE PARTITION ... WITHOUT VALIDATION` 让两个旧物理分区都含有 `(a,b,_tidb_rowid)=(1,1,1)` 后,`ALTER TABLE ... REORGANIZE PARTITION p0,p1 INTO (...)` 成功但 `COUNT(*)` 从 2 变 1。守卫格确认 same rowid/different raw bytes 会被修成新 rowid 且 2->2,same raw bytes/different rowid 也 2->2。源码锚点是 `/Users/bba/pc/tidb/pkg/ddl/partition.go:3906`: `BatchGetValue` 命中且 `bytes.Equal(vals, prr.vals)` 时直接 `continue`,把 raw-row equality 当成 "same row/already copied" 的 identity proof,但漏了 source physical partition identity。草案 `/Users/bba/pc/ai-native-reorg-duplicate-rowid-drop-draft.md`,方法 case `/Users/bba/pc/ai-native-id600001-method-case.md`。质量判断:高质量数据丢失,用户可见,强 oracle 是 rowset/cardinality invariant。暂停门:不要继续扩 reorg 语法变体;S9 只在另一个 equality-as-identity fast path 或修复验证时重开。

**2026-07-03 id30029 已入库为 candidate, S8 新增 AST-mutation 子形状但进入 guarded**。远端 `found_bug` 当前 `MAX(id)=30029,COUNT=31`,id30029=`prepared CREATE TABLE freezes non-strict VARCHAR auto-conversion across later strict sql_mode` 已 candidate 入库。testbed `8192975` 上, direct `SET sql_mode='STRICT_TRANS_TABLES'; CREATE TABLE ... c VARCHAR(70000) CHARACTER SET utf8mb4` 返回 `ERROR 1074`;direct 非 strict 会 warning 1246 并建成 `mediumtext`;但 `PREPARE` 在非 strict 下先发 warning 1246 并把 AST 改成 `TEXT`,随后切到 strict 后 `EXECUTE` 仍成功建 `mediumtext`。反向 strict 下 PREPARE 直接失败,`ALTER TABLE ADD COLUMN` 同形状未复现。草案 `/Users/bba/pc/ai-native-prepared-ddl-varchar-strict-freeze-draft.md`,方法 case `/Users/bba/pc/ai-native-id30029-method-case.md`。质量判断:行为差异真实,但 PREPARE 自身已发 auto-convert warning,prepared DDL 是否应冻结 PREPARE-time normalization 需要 owner/product contract 裁决,所以先作为 candidate,不计 confirmed。

**2026-07-03 id30028 已入库, 新增 S8 prepared/preprocess semantic freeze 规则**。远端 `found_bug` 当前 `MAX(id)=30028,COUNT=30`,id30028=`prepared statements bypass tidb_enable_noop_functions after the switch is turned off` 已 confirmed 入库。testbed `8192975` 上,`SET tidb_enable_noop_functions=OFF` 后 direct `SELECT SQL_CALC_FOUND_ROWS a FROM t ORDER BY a` 和 direct `SELECT a FROM t GROUP BY a DESC` 都返回 `ERROR 1235`;但同样 SQL 如果先在 `tidb_enable_noop_functions=ON` 下 `PREPARE`,再切到 `OFF` 后 `EXECUTE`,仍返回 rows 且 `@@warning_count=0`。`ADMIN FLUSH SESSION PLAN_CACHE` 后同一个 prepared statement 仍返回 rows,`@@last_plan_from_cache=0`,证明这不是 prepared plan cache key 漏项,而是 PREPARE-time preprocessor validation 被旧 AST/resolve context 冻住。源码锚点:`GeneratePlanCacheStmtWithAST` 在 PREPARE 时跑 `Preprocess(..., InPrepare, ...)`;`checkSelectNoopFuncs`/`checkGroupBy` 读取 `NoopFuncsMode`;`planCachePreprocess` 只在 schema version 变化时重跑 Preprocess。草案 `/Users/bba/pc/ai-native-prepared-noop-functions-freeze-draft.md`,方法 case `/Users/bba/pc/ai-native-id30028-method-case.md`。方法论收获:对于 prepared statement,要单独检查"PREPARE-time validator 消费 session variable,EXECUTE 是否按当前 session 重新验证";强 oracle 是 direct current-session reference vs prepared ON→OFF,再用 flush/off-cache 排除物理 plan cache。

**2026-07-03 id30027 已入库, S3 新增 cache key granularity 规则**。远端 `found_bug` 当时 `MAX(id)=30027,COUNT=29`,id30027=`inspection_result config details leak cached cluster_config rows across component types` 已 confirmed 入库。testbed `8192975` 上,直接查 `information_schema.cluster_config WHERE type='tikv' AND key='foo-test'` 只返回 `tikv-a=tikv-a,tikv-b=tikv-b`,但 `information_schema.inspection_result WHERE rule='config' AND item='foo-test' AND type='tikv'` 的 `details` 包含 `tidb-a config value is tidb-a`,且 `@@warning_count=0`。触发证据:`cluster_config` detail query 的 plan 显示 `type='tikv'` 被 `ClusterTableExtractor` 消费成 `node_types:["tikv"]`,只剩 `key='foo-test'` 作为 scalar Selection;但 `InspectionTableCache` 只按表名缓存第一次 full `cluster_config` snapshot,cache hit 后没有重新应用 node type 维度。草案 `/Users/bba/pc/ai-native-inspection-result-cluster-config-cache-draft.md`,方法 case `/Users/bba/pc/ai-native-id30027-method-case.md`。方法论收获:S3 的 shortcut/cache 证明义务必须覆盖 cache key 粒度;凡是 extractor 消费掉的维度(type/address/time/domain 等)没有进入 cache key,就必须在 cache hit 后重新检查。

**2026-07-03 id30026 已入库, S3 新增 type-domain conversion 规则**。远端 `found_bug` 当时 `MAX(id)=30026,COUNT=28`,id30026=`tikv_region_peers drops negative region_id/store_id predicates and returns all peers` 已 confirmed 入库。testbed `8192975` 上 `information_schema.tikv_region_peers` 总行数 269,但 `WHERE region_id=-1`、`WHERE store_id=-1`、`WHERE region_id IN (-1)` 都返回 269 行;CASE-wrapped reference 均返回 0。返回行投影 `region_id=-1` / `store_id=-1` 自身为 0,证明 SQL Selection 被吞。源码链路是 `extractCol` 提取并移除谓词后,`TikvRegionPeersExtractor` 用 `parseUint64` 转换 `region_id/store_id`;`ParseUint("-1")` 失败被静默忽略,内部 filter 变空但原谓词已丢。草案 `/Users/bba/pc/ai-native-tikv-region-peers-negative-id-draft.md`,方法 case `/Users/bba/pc/ai-native-id30026-method-case.md`。方法论收获:S3 的证明义务不只是"列/操作符可提取",还必须证明 SQL 值到 backend request domain 的转换成功/失败/空集合语义等价;否则必须保留 scalar recheck 或显式 `skip_request`。

**2026-07-03 id30025 已入库, S7 新增 coarse-key sufficiency 规则**。当时远端 `found_bug` 为 `MAX(id)=30025,COUNT=27`,id30025=`prepared plan cache reuses timezone-folded UNIX_TIMESTAMP literals across zones with same current offset` 已 confirmed 入库。testbed `8192975` 上,`Africa/Johannesburg` 与 `Europe/Amsterdam` 在当前日期同为 `+02:00`,但 `2025-01-15 12:00:00` 历史 offset 不同。先在 Johannesburg 下把 `SELECT UNIX_TIMESTAMP('2025-01-15 12:00:00')` 的 prepared plan cache 打到 `@@last_plan_from_cache=1`,再切 Amsterdam, direct/flush reference 返回 `1736938800`,cached execute 仍返回 Johannesburg 旧值 `1736935200`;反向 Amsterdam -> Johannesburg 同样复现。夏季同历史 offset 日期 `2025-07-15` 为 green。草案 `/Users/bba/pc/ai-native-plan-cache-timezone-unix-timestamp-draft.md`,方法 case `/Users/bba/pc/ai-native-id30025-method-case.md`。方法论收获:粗粒度 cache key 不是天然 bug;如果 hit path 重建语义边界就是 green,但如果命中后复用已折叠/已求值的旧语义,就必须证明 coarse key 覆盖了缺失细节。

**2026-07-03 id30024 已入库, S7 新增 semantic-switch coverage 规则**。当时远端 `found_bug` 为 `MAX(id)=30024,COUNT=26`,id30024=`prepared plan cache ignores tidb_sysdate_is_now semantic switch` 已 confirmed 入库。testbed `8192975` 上,同一个 prepared statement 包含 `sysdate()` 时,先在 `tidb_sysdate_is_now=0` 下执行到 `@@last_plan_from_cache=1`,再切到 `tidb_sysdate_is_now=1`,cached execute 仍返回旧语义 `sysdate(6)=now(6) => 0`;`ADMIN FLUSH SESSION PLAN_CACHE` 后同一个 prepared statement 立刻返回当前语义 `=> 1`。反向 `ON -> OFF` 同样复现:cache hit 继续 `=> 1`,flush 后变 `=> 0`。草案 `/Users/bba/pc/ai-native-plan-cache-sysdate-toggle-draft.md`,方法 case `/Users/bba/pc/ai-native-id30024-method-case.md`。方法论收获:S7 从“cache key completeness + payload purity”扩展到第三项“semantic-switch coverage”:凡是 session/config 变量会在 cached object 构造期改变表达式/计划语义,就必须进入 cache key 或触发 rebuild。

**2026-07-03 id30023 已入库, S3 新增 request/render context split 规则**。当时远端 `found_bug` 为 `MAX(id)=30023,COUNT=25`,id30023=`tidb_hot_regions_history update_time filter ignores session timezone in returned rows` 已 confirmed 入库。testbed `8192975` 上 `time_zone='+14:00'` 时,查询 `information_schema.tidb_hot_regions_history` 的等价绝对时间窗 `2026-07-03 13:40:41..13:40:42` 触发 `start_time/end_time` fast path 并返回 69 行,但返回行显示为 `2026-07-02 23:40:41`,同一行投影谓词 `update_time >= ... AND update_time < ...` 的 sum 为 0;CASE self-recheck reference 返回 0。UTC 等价窗口绿格返回 69 行且自谓词 sum=69。草案 `/Users/bba/pc/ai-native-hot-regions-history-timezone-draft.md`,探针 `/Users/bba/pc/ai_native_hot_regions_history_timezone_probe.py`,方法 case `/Users/bba/pc/ai-native-id30023-method-case.md`。方法论收获:shortcut 可以正确地按 session timezone 请求 backend,但在手工构造 SQL-visible row 时仍可能用错 context;`Time.In(tz)` 这类返回新值的转换函数如果未赋值,删掉 scalar recheck 后就是 wrong-result。

**2026-07-03 id30022 已入库, S3 新增 backend-not-found empty-rowset 规则**。当时远端 `found_bug` 为 `MAX(id)=30022,COUNT=24`,id30022=`tikv_region_peers region_id filter returns PD error for missing regions` 已 confirmed 入库。testbed `8192975` 上 `SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE region_id=0` 触发 `region_ids:[0]` fast path 后返回 PD `400 Bad Request`,而 CASE-wrapped reference 返回 0 行;`region_id IN (0,2)` 同样直接报错,但 reference 返回 region 2 的 3 行。已有 region `2` fast/reference 均为 3 行, sibling control `store_id=0` 正常返回 0。草案 `/Users/bba/pc/ai-native-tikv-region-peers-region-id-not-found-draft.md`,探针 `/Users/bba/pc/ai_native_tikv_region_peers_region_id_not_found_probe.py`,方法 case `/Users/bba/pc/ai-native-id30022-method-case.md`。方法论收获:把 SQL filter 委托给后端 point lookup 时,backend object-not-found 通常应映射为 SQL 空结果,不能当成整条 SQL 执行失败;IN-list 里单个 missing id 也不能吞掉 valid id。

**2026-07-03 id30021 已入库, S3 新增 interval-overlap coarse skip 规则**。当时远端 `found_bug` 为 `MAX(id)=30021,COUNT=23`,id30021=`statements_summary coarse time-range skip drops satisfiable interval-overlap predicates` 已 confirmed 入库。testbed `8192975` 上 `information_schema.statements_summary` 有窗口 `[2026-07-02 23:00:00,2026-07-02 23:30:00]`;选择 `A=23:10:00,B=23:20:00`,普通谓词 `summary_begin_time <= A AND summary_end_time >= B` 触发 `MemTableScan skip_request:true` 并返回 0 行,但 CASE-wrapped reference 返回 34+ 行,且每行两个投影谓词都为真。草案 `/Users/bba/pc/ai-native-statements-summary-coarse-range-draft.md`,探针 `/Users/bba/pc/ai_native_statements_summary_coarse_range_probe.py`,方法 case `/Users/bba/pc/ai-native-id30021-method-case.md`。方法论收获:row 是 interval/window 时,`start > end` 只说明 shortcut 的点范围为空,不证明原 SQL interval-containment 谓词不可满足;skip path 必须证明原谓词不可能成立。

**2026-07-03 id30020 已入库, 新增 S7 cache payload purity**。当时远端 `found_bug` 为 `MAX(id)=30020,COUNT=22`,id30020=`Apply cache reuses volatile scalar subquery results for duplicate correlated keys` 已 confirmed 入库。testbed `8192975` 上 `tidb_enable_parallel_apply=1`,`tidb_executor_concurrency=1`,`tidb_mem_quota_apply_cache>0` 时,相关子查询 `SELECT UUID() FROM inner_t WHERE inner_t.a=outer_t.a LIMIT 1` 在 duplicate outer key 上被 Apply cache 复用:`a=1` 的 24 行只有 1 个 distinct UUID,`a=2` 的 16 行只有 1 个;关闭 Apply cache 后分别恢复为 24/16 个 distinct UUID。确定性绿格 `CONCAT('v', inner_t.a)` 在 cache ON/OFF 都保持每 key 1 个值。草案 `/Users/bba/pc/ai-native-apply-cache-volatile-subquery-draft.md`,探针 `/Users/bba/pc/ai_native_apply_cache_volatile_probe.py`,方法 case `/Users/bba/pc/ai-native-id30020-method-case.md`。方法论收获:停止 id30019 的 helper 枚举后,切到新机制 Apply cache;新 selector S7 是“cache key 相同不等于 cached payload 可复用”,以后 cache/reuse fast path 必须同时证明 key completeness 和 payload purity。

**2026-07-03 id30019 已入库, S3 加了 helper blast-radius 停止规则**。当时远端 `found_bug` 为 `MAX(id)=30019,COUNT=21`,id30019=`metrics_summary METRICS_NAME extractor returns rows that fail binary equality predicate` 已 confirmed 入库,`ai-native-found-bug-pending.sql` 已清空为无待办。testbed `8192975` 上 `METRICS_NAME` 是 `utf8mb4_bin`,但 `WHERE metrics_name='TIDB_QPS'` 返回 `tidb_qps`,同一行投影 `metrics_name='TIDB_QPS'` 为 `0`;CASE-wrapped reference 为空;matching-case green control 为 `1`。草案 `/Users/bba/pc/ai-native-metrics-summary-name-case-draft.md`,探针 `/Users/bba/pc/ai_native_metrics_summary_name_case_probe.py`,方法 case `/Users/bba/pc/ai-native-id30019-method-case.md`。方法论收获:generic helper (`extractCol(..., valueToLower=true)` + 删除原谓词) 已跨第二个 owner 证明,所以这里记录一个代表性 blast-radius case 后必须停止枚举所有 `valueToLower=true` 调用点。

**2026-07-03 id30018 已入库, S3 选择器继续被验证**。id30018=`InfoSchema scalar-pushdown extractor returns rows that fail LOWER/UPPER predicate` 已 confirmed 入库。testbed `8192975` 上 `LOWER(table_name)='ACASE'` 返回 `Acase`,但同一行投影 `LOWER(table_name)='ACASE'` 为 `0`;CASE-wrapped reference 为空;`UPPER(table_name)='acase'` 对称复现。草案 `/Users/bba/pc/ai-native-infoschema-scalar-pushdown-case-draft.md`,探针 `/Users/bba/pc/ai_native_infoschema_scalar_pushdown_case_probe.py`,方法 case `/Users/bba/pc/ai-native-id30018-method-case.md`。方法论收获:S3 不只是"LIKE/collation",而是更一般的"shortcut extractor 同时做 scalar pushdown + value/key normalization + 删除原谓词";强 oracle 是 row self-predicate + CASE reference + green control。

**2026-07-03 id30017 入库后补了两个负样本校准**。id30017=`EXCHANGE PARTITION leaks a stats lock to the exchanged table` 已 confirmed 入库。随后继续方法论校准:一是 S5 小格 `REMOVE PARTITIONING` → `PARTITION BY KEY ... UPDATE INDEXES (idx_a GLOBAL)` 为 GREEN,`USE INDEX`/table scan/`ADMIN CHECK` 均一致,说明 id30007 的高风险集中在带 non-touched phase 的 sibling iterator,不是所有 partition/global-index 迁移;二是 S3 候选 `tidb_hot_regions_history.update_time = ts.000500` 为 GREEN,虽然 EXPLAIN 显示秒级窗口,执行层没有返回 `ts.000000` 的 scalar-false 行,所以“显示精度降低”本身不能算 RED,必须用 row-level self predicate/CASE oracle 证明。

**2026-07-03 S4 再次命中(id30017, stats lock × EXCHANGE PARTITION ID swap)**。在 DDL 普通矩阵全绿后,继续按“side state owner key + ID swap”挖,没有扩 happy path。源码证明义务:`LOCK STATS t` 把 table/partition 解析成 `mysql.stats_table_locked.table_id`,`EXCHANGE PARTITION` 会交换 standalone table ID 与 partition ID,`SHOW STATS_LOCKED` 再用当前 InfoSchema 反查 ID。现有测试 `TestExchangePartitionShouldChangeNothing` 只检查 lock row count,没检查 SQL 可见对象映射和 lock/unlock round-trip。testbed `8192975` 复现:`LOCK STATS t` 后 `SHOW STATS_LOCKED` 为 `t/global,t/p0,t/p1`;执行 `ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1` 后变成 `t/global,t1,t/p1`;随后 `UNLOCK STATS t` 只清掉当前 `t` 相关锁,`t1` 仍显示 locked。草案 `/Users/bba/pc/ai-native-stats-lock-exchange-partition-draft.md`,探针 `/Users/bba/pc/ai_native_ddl_stats_lock_exchange_partition_probe.py`,方法 case `/Users/bba/pc/ai-native-id30017-method-case.md`。方法论收获:side-state owner 的 oracle 不能只数行数,必须把 ID 映射回 SQL 可见对象,再用成对命令/cleanup 行为验证 owner 没跑偏。

**2026-07-03 DDL 普通 owner 矩阵再次全绿,方法论继续收窄到“非对称证明义务”**。在 testbed `8192975` 上重跑两类已沉淀矩阵:`ai_native_ddl_reference_matrix_probe.py` 28 格 `findings=0 known_controls=3 skipped=0`,`ai_native_ddl_object_reference_probe.py` 17 格 `findings=0 skipped=0`;另补 TTL×FK 对称控制格,即使用 `foreign_key_checks=0` 先建 dangling child FK,后续 `CREATE TABLE parent ... TTL` 仍被 `8152` 阻断。masking-policy × recover 静态上有 drop 清理/rename-truncate 同步/recover 不恢复的差异,但当前代码只在 DDL 自身消费 masking policy,没有查询层行为 oracle,所以降为边界。方法论收获:DDL 普通 rewrite/block 矩阵已接近饱和,下一步不要横向扩 rename/drop/partition happy path;候选必须证明入口间有非对称 validator/sanitizer、rollback/restore 重建丢失 owner bit,或独立 sibling iterator。

**2026-07-03 S6 反杀/边界样本:sequence-default recover 不是 validator-gap 新 bug**。继续 DDL-only 后,按 id30016 的 validator-gap 规则尝试 sequence default recover:`DROP TABLE t; DROP SEQUENCE seq; FLASHBACK TABLE t` 会恢复出 `DEFAULT (nextval(db.seq))`,后续默认插入报 1146。但关键控制格显示 `CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR missing_seq)` 本身也成功,只是 insert 时失败;因此这不是"recover 绕过普通 create validator",而是 id30005 已记录的 lazy-name-resolution/executable default 家族。探针 `/Users/bba/pc/ai_native_ddl_sequence_recover_boundary_probe.py` 输出 `SUMMARY total=3 findings=0 boundary=1`。方法论收获:恢复后坏掉不等于 S6 红格;必须先证明普通路径有 validator,否则只能记 INFO/boundary,不能入库。

**2026-07-03 回到 DDL-only 后 S6 再次命中(id30016, FK FLASHBACK TABLE missing parent)**。按用户纠偏,不继续执行器/optimizer 扩散,回到 restore-path reference owner。S6 小屏先校准边界:TTL recover table/schema 都会把 TTL_ENABLE 置 OFF(绿);cached table 被 `DROP TABLE` 阻断,所以 recover 不可达(绿/blocked);TiFlash recover 静态高信号但当前 testbed 无 TiFlash store/PD placement target,只能标 environment-blocked。随后打 FK:普通 create path 会校验 parent,`CREATE TABLE c ... REFERENCES p` 在缺父表时报 1824;但 `DROP child; DROP parent; FLASHBACK child` 能恢复出 `SHOW CREATE TABLE c`/`information_schema.key_column_usage` 都声明 `REFERENCES p` 的子表,父表缺失期间 `INSERT INTO c VALUES (2,999)` 成功,`EXPLAIN INSERT` 没有 `Foreign_Key_Check`;重建父表后新的非法插入又失败,但旧孤儿行保留,`ADMIN CHECK TABLE c` 仍 OK。草案 `/Users/bba/pc/ai-native-fk-flashback-missing-parent-draft.md`,探针 `/Users/bba/pc/ai_native_ddl_fk_flashback_missing_parent_probe.py`,已写入远端 `found_bug id30016`。方法论收获:S6 不应枚举所有 restored fields,应先筛“普通 create/alter 有显式 validator + recover 绕开 validator + post-recover 行为 oracle”,FK 同时满足三项,所以首轮小矩阵即红。

**2026-07-03 S3 新 D 维度命中(id30015, cluster_log sub-ms precision)**。继续按 shortcut/extractor selector,但避免在 InfoSchema/collation 旧家族里打转:先证伪了两个近邻——`cluster_info upper/lower(type)` 没触发 extractor(计划保留 Selection),普通表前缀索引也保留 full predicate/order safe path；随后转向 `cluster_log` 的时间精度证明义务。源码显示 `extractTimeRange` 按 DATETIME(6) 抽出 `time = const` 后删除原谓词,而 `ClusterLogTableExtractor` 又把 start/end `/ time.Millisecond` 截断后发给日志搜索。实锤:自动探针选中 `2026/07/02 22:00:45.416`,构造 `2026/07/02 22:00:45.416500`,普通 `WHERE time = ...416500 AND message LIKE '%'` 返回 2 行 `.416`,且返回行自身 `time = ...416500` 全为 0；CASE oracle 在同一毫秒窗口返回 0。草案 `/Users/bba/pc/ai-native-clusterlog-subms-precision-draft.md`,探针 `/Users/bba/pc/ai_native_clusterlog_subms_precision_probe.py`,已写入远端 `found_bug id30015`。方法论收获:S3 新增"precision-lowering guard"——任何把高精度 SQL 谓词压成低精度后端请求的 extractor,如果删掉原谓词,必须有 scalar recheck 或精确保真。

**2026-07-03 PS1 第三次命中(perf-30003 / id30014, ANALYZE lifecycle)**。继续用 PS1 打 bespoke 后台 loop,但这次没有把"partial stats"误判成 bug,而是把 oracle 换成**visible sub-job lifecycle + processlist liveness**:partitioned `ANALYZE TABLE` 在 `analyzeBeforeSendToSaveResults=2*off->pause` 后被 `KILL QUERY` 中断,客户端返回 `ERROR 1317`,但 `mysql.analyze_jobs`/`SHOW ANALYZE STATUS` 仍残留一个 partition job 为 `running`,其 `process_id` 已不在 `information_schema.processlist`,干净重跑 ANALYZE 也只是追加 finished rows,不会立刻清掉旧 running 行。两次干净库复跑稳定。源码链路:`StartAnalyzeJob` 在 analyze worker,`FinishAnalyzeJob` 主要由 save worker 持有;kill check 位于结果送入 `saveResultsCh` 之前,导致已 started 但未 handoff 的 job 绕过 finish。草案 `/Users/bba/pc/ai-native-perf-analyze-interrupt-running-job-draft.md`,探针 `/Users/bba/pc/ai_native_perf_pf6_analyze_interrupt.py`,已写入远端 `found_bug id30014`。方法论收获:PS1 从"checkpoint/rework"扩展为"progress/lifecycle ownership";多组件后台流水线要检查每个 started visible sub-job 在父操作返回时是否 terminal。

**2026-07-03 自主循环 P1 tick:O4 被硬化为 O4',但不虚升 TRUSTED**。按用户要求"每轮一个 subagent",Zeno 先做 O4(no-shortcut/reference oracle)对抗审计,主线程在 testbed `8192975` 执行最小矩阵。结论:O4 能干净确认 id30010,但必须硬化分类规则,否则会把 id30013 这类 contract-ambiguous split 误当 confirmed。实测四格:RED=`aio4_red` 中 fast `table_name_pattern:[a_%]` 返回 `Acase,a%b,a_b`,CASE scalar reference 只返回 `a%b,a_b`,且 `Acase LIKE 'a_%'=0`;GREEN(triggered)=全小写 control fast/ref 都返回 `a_b,a_c`;INVALID=`LOWER(table_name) LIKE 'a_%'` 虽绕过 extractor 但改变语义,不能当 reference;INFO=`cluster_log.type='PD'` 触发 `node_types:["pd"]` 返回 63844 行 `pd`,而 `type LIKE 'PD'` scalar 为 0,但诊断表枚举是否有意大小写不敏感仍需 O6/owner contract。已更新 `/Users/bba/pc/ai-native-oracle-library.md` 的 O4' 和 `/Users/bba/pc/ai-native-autonomous-loop.md` 的第二个 P1 tick。方法论收获:一个 tick 不一定要产新 bug;让 oracle 学会 RED/GREEN/INVALID/INFO,比继续用坏仪器挖更多"疑似 bug"更接近全自动化。

**2026-07-03 PF-5 查询优化器模块(非 checkpoint 类,work+choice claim)=全 GREEN,诚实下调该家族**。用户要求换掉 PS1/进度类,改打优化器。target-sourced 用 diff-directed(近期 planner 提交)+D_dims battery(out-of-range、correlation)。测遍:分区剪枝(eq/IN/range/OR/函数/子查询/null-safe<=>,dynamic==static 一致)、null-safe 剪枝(#68425 邻域完整)、TopN 下推(Limit build=10x8,fan-out bound=8 符合 #67676)、dynamic vs static 的 processed_keys 一致、相关列选更选择性索引、out-of-range 20000x 低估(join 选 HashJoin 未落退化 index join;聚合 IndexLookup 实测 RU 41<152 反而更便宜)。唯一看似活的假设(out-of-range→index/scan 误选)被**实际成本测量(RU/time)反驳**,与 O6 反驳 rate-limit 假线索同理——不制造边际 bug。**Reopen**:cop-request 在途并发/burst(#67676 领域)在 slow_query/EXPLAIN 不可观测,需 metrics-diff harness(=L3 isolation-oracle 缺口),那才是查询层剩余 perf 密度所在;随机 vs 顺序的纯成本误选是 PO1 设计内盲区。探针未留脚本(手动矩阵),记录见 perf-oracle-library campaign log PF-5。

**2026-07-03 PS1 第三模块=GREEN 边界(PF-4,DXF ingest),细化了 selector 而非再出一个 bug**。用 PS1 打 IMPORT INTO/DXF:发现(a)`IMPORT INTO ... FROM SELECT` 是内联管道(importFromSelect+errgroup,不走 DXF、无 checkpoint),文件导入才用 DXF 但本环境无文件源;(b)经 fast-reorg add index 触达共享的 DXF backfill 框架,它**持久化 checkpoint**(`mysql.tidb_ddl_reorg.reorg_meta`+`adjustStartKey` 跳过已处理 range,resume 时 LoadCheckpoint)——这正是 txn 路径(perf-30001,start_key 冻结在 handle 1)和 TTL scan(perf-30002,无 scan-key)所缺。**关键收获是边界**:PS1 的 bug 类不是"任何被中断的后台任务",而是"predate/bypass DXF checkpoint 框架的 bespoke/legacy 循环";下一轮该打剩余 bespoke 循环(GC/ANALYZE/auto-analyze),不该打 DXF 框架任务。GREEN 未完全执行验证(诚实标注):PO4 的 row_count 守恒 oracle 对 DXF ingest 结构性失明(row_count 全程 0、完成才跳 N,永不 2N=harness lesson L8),且 checkpoint flush 10s+单 subtask watermark 只在收尾推进,短 run 里看不到推进。三次 INVALID 全被触发证据守卫正确拦下(拒绝把 vacuous ratio=1.000 读成 GREEN)。探针 `ai_native_perf_pf4_dxf_ingest_checkpoint.py`/`ai_native_perf_pf4_dxf_checkpoint_live.py`,边界表见 perf-oracle-library "Where the checkpoint discipline exists vs is missing"。**PS1 战绩:3 提名 → 2 RED(bespoke)+1 GREEN 边界(DXF 框架),跨 3 子系统,selector 已细化。**

**2026-07-03 性能 loop 换模块二次命中(perf-30002,TTL)**。同一个 selector PS1(后台任务中断×进度状态重建)从 DDL backfill 迁到 TTL 模块再次命中——**一个 selector 跨两个子系统各出一个 bug,证明它跨模块预测而非单模块过拟合**。发现:TTL scan task 被重新 lock 时(心跳超时/owner failover/重启)从 `ScanRangeStart` 全量重扫,无 range 内进度 checkpoint。源码锚点:`TTLTaskState`(cache/task.go:112)只有 TotalRows/SuccessRows/PreviousOwner 无 last-scanned-key;`NewScanQueryGenerator`(scan.go:212)恒从 ScanRangeStart 播种;re-lock 时 statistics 全新归零(task_manager.go:476)。实测(HB 超时注入,ai_native_perf_pf3_ttl_rescan.py):re-lock 后出现一条**无 `_tidb_rowid` 下界的 range-start scan**,total_keys=21145/skip=16384,是正常批次 481 的 44 倍(re-seek 掉已删前缀);触发证据 owner 从 fake 变回真实=True;天然 specificity 对照=job 启动时同形态查询 skip≈0。严重性诚实定级:比 perf-30001 轻(按 region 有界、中断触发非常规操作、无正确性影响、无重复删除),但真实的"缺 checkpoint→中断即重做"缺口。CLOSED-FIXABLE,fix=在 tidb_ttl_task.state 持久化续扫 key,V2 关键约束=checkpoint 必须取 in-flight delete 进度的下界而非 scan 游标(否则把重做换成漏删更糟)。草案 `/Users/bba/pc/ai-native-perf-ttl-rescan-checkpoint-draft.md`,oracle PO5 已注册。过程中参考差分(O6)杀掉一条假线索:`tidb_ttl_delete_rate_limit=200` 让 13 万行只跑 22s 一度像限流失效,但官方文档定义单位是 delete-**operations**/s(每条 DELETE 取 1 token),实现与契约一致→info(contract-ambiguous)不入库。观测面第七次反杀(L7:worker-count 中断原语因 MinValue=1+workers[0] 恒保留而失效,触发证据守卫两次拦下假 RED;cursor-min 检测器有 FN,改用无下界查询检测才干净复现 RED)。

**2026-07-03 性能 sibling loop 首战命中(perf-30001)**。按 v2 §Bug-Class Scope 把性能循环从纸面变成实战:新建 perf oracle 库 `/Users/bba/pc/ai-native-perf-oracle-library.md`(PO1 forced-plan 差分/PO2 growth-shape/PO3 cache-drift/PO4 rework-conservation,四种 claim 形状 work/choice/reuse/progress),严格走"仪器先行"——PO1/PO3 过对抗校验+held-out(sensitivity+specificity 4/4,harness `/Users/bba/pc/ai_native_perf_po_heldout.py`)后才开挖。观测面反杀六次(collect_execution_info 关闭/copr cache 污染重复执行/常量传播折叠 defeat 谓词/binding 覆盖 hint/大 IndexLookup 二次执行必污染/匹配串碰撞),全部以可执行子句闭环,写入库文件 L1-L6——trigger-evidence 递归原则的 perf 版铁证。挖掘:PF-1(plan cache×分区表)被 cacheability guard 拦住(v8.4 分区表 prepared plan 完全不进 cache,green 校准+reopen 条件);转 PF-2(PO4+ADMIN PAUSE/RESUME)**首矩阵即红**:txn 模式(fast_reorg=0)下 ADMIN PAUSE 不停 backfill、中途 checkpoint 从不持久化(start_key 冻结在 handle 1)、RESUME 从头全量重扫,final row_count 恒=2N(两个 pause 点、两个表规模复现;无 pause 控制组恒=N;默认 ingest 路径 pause/resume 干净 ratio=1.000)。正确性无损,纯性能+可观测性缺陷。已 CLOSED-FIXABLE:草案+P/Q/D/F+源码锚点(reorg.go:395 记账续种/job_worker.go:809 pause 不传播/checkpoint 冻结)+fix-validation contract 见 `/Users/bba/pc/ai-native-perf-addindex-pause-rework-draft.md`。新 selector PS1(后台任务中断 sibling path×进度状态重建)= id30009 的跨循环迁移,首次跨循环 selector 转移命中。开放探针:mid-ingest pause、owner-switch/restart 变体、PO4 monotonicity 形式的注入 held-out。上游去重:#56942(DXF 显示型)/#25968(2021 记账)均不覆盖本发现。
**2026-07-02 终极方向:完全自动化自我迭代挖掘**。方法论完善的目标是让 agent 无人值守持续挖下去。规范见 `/Users/bba/pc/ai-native-autonomous-loop.md`(自主循环:SENSE→SCHEDULE→ACT→INTEGRATE→HEALTH→GATE)。核心判断:**自动化瓶颈不是循环逻辑,而是判定可信度**——所以调度优先级把"判定工具健康"(P0 修 REFUTED oracle / P1 验证 confirm 过 bug 的 oracle / P2 对抗校验 / P3 补缺失 oracle)排在"挖新 bug"(P4)之前;一个用坏 oracle 高速挖 bug 的循环是失败模式。oracle 库/selector 台账/catalog/found_bug 是循环的状态存储,唯一工程缺口是把它们从 markdown 变成机器可读。这几轮的 oracle 验证工作(对抗校验、held-out、refutation 三分类、trigger-evidence 递归)正是自动化的前提。当前按规范第一个 tick:P1 触发,先把 O9'/O2' 验到 TRUSTED,不去挖新 bug。


**2026-07-02 S3 第三次预测 + MySQL 参考仲裁(id30013 candidate)**:继续用 S3 扫下一个 extractor,命中 collation 子维度:`extractCol(valueToLower=true)` 把用户值 lowercase 且 drop 原谓词,而 `cluster_log/cluster_info/inspection_rules` 的 `type` 列是 `utf8mb4_bin`(大小写敏感)→ `type='PD'` 返回 197 行全是 `pd`,违反列 collation。**关键裁决:candidate 不是 confirmed**——`valueToLower=true` 对固定枚举列可能是有意便利。用用户建议的 **MySQL 参考差分**消解不确定:MySQL 8.3.0 和 TiDB 普通表对 bin 列 `='PD'` 都返回 0(大小写敏感),所以"bin 列 = 大小写敏感"是铁契约、cluster_log 客观违反;只剩"诊断表是否有意豁免"留给 owner。强证据:同名列 `type` 跨 extractor 不一致(true@737/805/1271, false@942)。探针 `/Users/bba/pc/ai_native_clusterlog_type_collation_probe.py`(输出 INFO(contract-ambiguous)),草案 `/Users/bba/pc/ai-native-clusterlog-type-collation-draft.md`。已补入远端 `found_bug id30013`(`candidate`)。S3 现 3/3 预测命中真实语义违反(2 confirmed + 1 candidate)。**方法论演进**:v2 新增"参考实现差分"oracle(见 methodology-v2.md Strong Oracle Patterns),用于把 contract-ambiguous cell 切分成"参考实现确定的通用契约"与"需 owner 裁决的产品豁免"。暂停门:不要继续扩 level/rule/metrics_name 变体(那是 id30013 blast radius)。后续 id30019 只作为 generic helper 的第二 owner 代表样本,不是重开 id30013 变体枚举。

**2026-07-02 方法论 v2 第二个命中(id30012),换 selector 验证泛化**:id30011 暂停门后没有继续扩 restore 变体,而是换 S3 selector(shortcut/extractor 有损预过滤,源自 id30010)测泛化。一次 grep 就从源码发现不对称:`ClusterLogTableExtractor` 用 `time.Local`(memtable_predicate_extractor.go:816),而 slow_query(1334)/metrics(1048)/statements_summary(1626) 全用 session `StmtCtx.TimeZone()`;且匹配的时间谓词被从 `remained` 移除(无 scalar 兜底)→ 非 UTC session 下 `information_schema.cluster_log` 时间过滤 wrong-result。双向铁证:`+14:00` 与 `+00:00` 同字面量返回逐行相同 415 行(返回违反 WHERE 的行);尊重时区的 `+14:00` 字面量返回 0(漏掉满足 WHERE 的行)。**id30012 confirmed**。修复:816 行 `time.Local`→`StmtCtx.TimeZone()`。探针 `/Users/bba/pc/ai_native_clusterlog_timezone_probe.py`(可回归),草案 `/Users/bba/pc/ai-native-clusterlog-timezone-draft.md`,方法 case `/Users/bba/pc/ai-native-id30012-method-case.md`。**S3 已 2/2 命中(id30010+id30012),是目前最强 selector**;台账 `/Users/bba/pc/ai-native-selector-ledger.md` 已更新,新增电池条目"sibling 不对称:N 个兄弟用 X、一个用 Y"。暂停门:不再扩时区变体。

**2026-07-02 方法论 v2 首次实战(id30011)**:按 `/Users/bba/pc/ai-native-proof-obligation-methodology-v2.md` 的完整流程(台账选目标→审计卡+T_tests→小矩阵+触发证据→暂停门)跑了第一轮:S1(sibling-path 重建)+S2(引用无反向扫描)两个 selector 联合提名"restore 路径引用再校验",源码确认 `recoverTable` 会清 placement ref(`clearTablePlacementAndBundles`)而 `onRecoverSchema` 原样写回 DBInfo 且无校验,T_tests 为零覆盖。6 格矩阵首轮命中 2 红格:**id30011 confirmed** — `FLASHBACK DATABASE` 恢复出引用已删 policy 的库,`SHOW CREATE DATABASE` 带 dangling ref,库内 `CREATE TABLE` 全部报 8239,直到手工 `ALTER DATABASE ... PLACEMENT POLICY=DEFAULT`。6 条 SQL 最小复现、源码链路、修复方向见 `/Users/bba/pc/ai-native-flashback-db-placement-draft.md`;审计卡 `/Users/bba/pc/ai-native-flashback-placement-audit-card.md`;方法 case(v2 效率论证)`/Users/bba/pc/ai-native-id30011-method-case.md`;selector 台账首版 `/Users/bba/pc/ai-native-selector-ledger.md`(新 selector S6: restore-path container bypass)。已入库 `found_bug id30011`(`confirmed=1,status=confirmed`)。**暂停门:不要继续扩 restore/flashback 变体(BR/IMPORT、FLASHBACK TABLE TO、TiFlash replica 字段等已列入 S6 队列):GUARDED 防 blast-radius**。已 CLOSED-FIXABLE,修复方向倾向 `onRecoverSchema` 与 sibling 路径一致地清 DB 级 ref,或校验后置 nil+warning。注意同根伴生不对称:policy 存活时 flashback 的库保留 DB 级 ref 而库内表丢失表级 ref。

**2026-07-02 非 DDL 模块切换补充**:用户明确要求"切换到别的模块挖掘 bug"后,当前方法论从 DDL object-reference lane 扩展出一个新的高效 selector:

```text
custom shortcut path / extractor / cache
→ 找它必须保持的 SQL 语义
→ 用 CASE-wrapped / no-shortcut / safe-path 形式做强 oracle
→ 小 adversarial name/value set 扫红格
```

这个非 DDL shortcut/extractor 分支先由 **id30010** 验证: `information_schema.tables.TABLE_NAME`/`columns.TABLE_NAME` 的 SQL-visible collation 是 `utf8mb4_bin`,但 InfoSchema predicate extractor 对 `table_name LIKE 'a_%'` 做了 case-insensitive/lowercase 预过滤并移除了原始谓词,导致 `Acase` 这种不满足二进制 LIKE 的表名仍被返回。随后 **id30018** 验证了同一 S3 selector 的新子形状: `LOWER/UPPER(TABLE_NAME)` scalar pushdown 与 value/key normalization 组合后也能返回自谓词为 false 的行。后续 id30021-id30023/id30026/id30027 继续把同一方法推进到 interval skip、backend error-domain、request/render context、type-domain conversion、cache key granularity。这次切模块不是回到随机 fuzz,而是验证了"证明义务 + 强等价 oracle + 小矩阵"同样适用于 system table / virtual table shortcut/cache。

**2026-07-01 方向纠偏**:当前主线已收束回 **DDL-only**。id30001/id30002 属于 proof-obligation 方法论的重要证据,但 id30002 已经漂到 optimizer/predicate simplification,下一轮不要继续沿 optimizer/executor 扩。现在的目标是围绕:

```text
DDL 改对象后,所有引用必须 rewrite 或 block
```

构建 `column/table/index/partition` ALTER 路径的 reference ownership matrix,优先打 `RENAME/CHANGE/DROP/MODIFY COLUMN` 与 `CHECK / partial index / partition / generated / TTL / FK / placement / global/local index` 的交叉。新的工作入口:

- `/Users/bba/pc/ai-native-ddl-reference-matrix.md`
- `/Users/bba/pc/ai_native_ddl_reference_matrix_probe.py`
- `/Users/bba/pc/ai_native_ddl_object_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_stateful_object_probe.py`
- `/Users/bba/pc/ai_native_ddl_delete_range_probe.py`
- `/Users/bba/pc/ai_native_ddl_placement_bundle_failure_probe.py`
- `/Users/bba/pc/ai_native_ddl_fk_object_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_masking_policy_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_stats_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_privilege_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_region_split_policy_probe.py`
- `/Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_affinity_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_db_placement_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_view_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_resource_group_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py`
- `/Users/bba/pc/ai-native-id30004-method-case.md`
- `/Users/bba/pc/ai-native-sequence-default-reference-draft.md`
- `/Users/bba/pc/ai-native-id30005-method-case.md`
- `/Users/bba/pc/ai-native-hypo-index-reference-draft.md`
- `/Users/bba/pc/ai-native-id30006-method-case.md`
- `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md`
- `/Users/bba/pc/ai-native-id30007-method-case.md`
- `/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md`
- `/Users/bba/pc/ai-native-id30008-method-case.md`
- `/Users/bba/pc/ai-native-add-index-global-rollback-delete-range-draft.md`
- `/Users/bba/pc/ai-native-id30009-method-case.md`
- `/Users/bba/pc/ai-native-infoschema-like-case-sensitive-draft.md`
- `/Users/bba/pc/ai-native-id30010-method-case.md`
- `/Users/bba/pc/ai-native-infoschema-scalar-pushdown-case-draft.md`
- `/Users/bba/pc/ai_native_infoschema_scalar_pushdown_case_probe.py`
- `/Users/bba/pc/ai-native-id30018-method-case.md`
- `/Users/bba/pc/ai_native_partition_global_index_prune_probe.py`
- `/Users/bba/pc/ai_native_plan_cache_drift_probe.py`
- `/Users/bba/pc/ai-native-ddl-next-owner-scan.md`

执行器/optimizer 只能作为 DDL 后验 oracle,不能作为新主线目标。

方法论已走完"挖场景/不变量 → 落 oracle → 外部驱动执行 → 抓到静默 bug"全链路,且**关键能力都已验证**:
- **LINCHPIN 已验证**:`/fail` 的 `pause` 能从外部 hold 住 add-index 的 reorg 子状态 → 种子引擎可外部驱动。
- **TRUE-positive 已坐实**(planted-bug):挖出的 `ADMIN CHECK`/差分 oracle 抓到了 `ALTER` rc=0 静默成功、但索引漏 500 行的不一致(8223)。
- **非 planted 真命中继续推进**:推理驱动矩阵/selector 已找到 29 个 id300xx confirmed/candidate 新 bug,另有 id600001/id630001-id630014 DDL/正确性系列。当前 DDL 最新 confirmed 是 id630014: `EXCHANGE PARTITION` 交换 standalone table ID 与 partition physical ID 后,masking-policy sys row 仍显示旧逻辑表名但挂到旧 ID,导致 `DISABLE/DROP MASKING POLICY` 无法触达旧策略。上一条 DDL 高质量数据完整性 bug 是 id630013:`MODIFY COLUMN` 可把已有 CHECK-valid 行转换成违反同一 CHECK 的值,例如 `DECIMAL 0.40 -> INT 0` 后 `CHECK(a > 0)` 仍发布但行自身谓词为 false。当前非 DDL 最新入库是 id1230001 confirmed:NT-DML 在 `SET TRANSACTION READ ONLY AS OF TIMESTAMP` 后用 stale `tx_read_ts` 切 split range,`BATCH UPDATE` 成功但漏改当前新行;这是 S23 state-ingress 命中。S8 最新仍是 id30029 candidate:prepared `CREATE TABLE` 在非 strict PREPARE 时把过长 `VARCHAR` auto-convert 成 `TEXT`,切 strict 后 EXECUTE 仍创建 `mediumtext`;direct strict reference 会 1074。id30028 confirmed 是 prepared statement 在 `tidb_enable_noop_functions=ON` 下 PREPARE 后,切到 `OFF` 仍能执行 direct SQL 会被 1235 拒绝的 `SQL_CALC_FOUND_ROWS` / `GROUP BY expr DESC`;flush plan cache 后仍复现,证明不是 plan-cache key,而是 PREPARE-time preprocessor semantic freeze。当前 DDL 主线新增暂停门是 S4/id630014:不要扩 masking-policy 基础 rewrite/cleanup,只找另一类 ID-swap/move-rekey owner 或 fix validation;S17:不要枚举所有类型转换,只在另一个 raw DDL writer 或 row invariant owner 上重开;S16:id630011/id630012 之后也不要继续枚举 FK 维度,只找"缺失维度没有后置完整 target-state validator"的 validator-ordering/proof-precision gap;其他独立暂停门仍包括 id30017 stats lock ID swap、id30016 FK FLASHBACK TABLE missing parent、id30009 add-index/global-index rollback delete-range,id30007 reorg/global-index、id30006 hypo-index session metadata、id30005 sequence-default reference。
- 基础设施常驻:集群上 failpoint TiDB(`fp-tidb`)是唯一 tidb + DDL owner,`/fail/` 动态控制可用。

**当前节奏(重要)**:DDL-only 纠偏已经进入 object-reference matrix。column-reference 小矩阵 28 格复现 3 个已知红格且无新发现;object-reference 小矩阵 17 格覆盖 placement policy、`ALTER PLACEMENT POLICY` dependent refs、partition drop/truncate、global/local index 以及 placement+global 混合普通路径,同样无新发现。stateful 探针已在 failpoint-enabled `fp-tidb` 上跑 14 格:`SUMMARY total=14 findings=0 skipped=0`,覆盖 `reorgPartRollback2/3/4` × `PARTITION BY ... UPDATE INDEXES` / `REMOVE PARTITIONING`,`reorgPartFail4/5` 一次性失败重试,以及 `truncatePartCancel1`/一次性 `truncatePartFail1..3`。delete-range metadata 探针新增 2 格:`SUMMARY total=2 findings=0 skipped=0`,确认 `REMOVE PARTITIONING` 会登记旧 global index range,`DROP GLOBAL INDEX` 不会误登记 table/partition range。placement-bundle failure 探针新增 5 格:`SUMMARY total=5 findings=0 skipped=0`,确认 persistent PD bundle failure 不污染 table/partition/policy metadata,一次性普通错误会 retry 并保持 dependency。FK table/index object 探针新增 10 格:`SUMMARY total=10 findings=0 skipped=0`,确认 parent/child/multi-table rename 会 rewrite/preserve FK table refs,drop parent/truncate parent/drop supporting index 会由 FK owner block,rename supporting index 和 drop redundant index 保持 FK 行为。masking-policy side-metadata 探针基础 13 格:`SUMMARY total=13 findings=0 skipped=0`,确认 table/cross-db/multi-table rename 会 rewrite policy refs,column rename/multi-schema change 会 rewrite `column_name` 与 expression,unsupported modify 会 block,drop column/table/database 会 cleanup,truncate 会 rewrite `table_id` 且 policy 仍可操作;2026-07-03 新 entrypoint 复查命中 id630014:`EXCHANGE PARTITION` table/partition ID swap 后 policy row 悬挂且无法 DISABLE/DROP。stats side-metadata 探针新增 7 格:`SUMMARY total=7 findings=2 skipped=0`,红格是 `RENAME COLUMN a TO aa` 和 `CHANGE COLUMN a aa INT` 后 live schema 已只有 `aa`,但 `SHOW STATS_HISTOGRAMS` 仍显示旧列名 `a` 直到重新 `ANALYZE TABLE`;两格视为同一根因族,草案见 `/Users/bba/pc/ai-native-stats-column-rename-draft.md`。stats 暂停门已完成到可讨论质量,不要继续扩 stats 矩阵。privilege side-metadata 筛选探针新增 3 格:`SUMMARY total=3 findings=0 skipped=0`,结论是 `mysql.tables_priv`/`mysql.columns_priv` 是名字绑定 policy,不是 DDL object-reference owner。table-cache side-metadata 探针新增 3 格:`SUMMARY total=3 findings=1 skipped=0`,绿格确认 `CACHE` 写入 table-id side row、`NOCACHE` 清理、cached table 对 rename/drop/truncate/index/partition DDL 均 block;红格是 `DROP DATABASE` 成功但留下 `mysql.table_cache_meta` orphan table ID,草案见 `/Users/bba/pc/ai-native-table-cache-drop-database-draft.md`。region-split policy 负样本探针新增 5 格:`SUMMARY total=5 findings=0 skipped=0`,确认 split policy 跟随 `TableInfo`/`IndexInfo` 自然 rename/drop/跨库 rename,drop+add 不泄漏,列 change 后仍可 `SHOW CREATE` round-trip。sequence-default reference 探针新增 5 格:`SUMMARY total=5 findings=3 skipped=0`,红格是 `DROP SEQUENCE`、`RENAME TABLE seq TO seq2`、跨库 `DROP DATABASE` 均能留下 live table default 指向 missing sequence,绿格证明 live default 和 `CHANGE COLUMN ... DEFAULT NEXT VALUE FOR seq` 本身正常。affinity reference-owner 筛选探针新增 6 格:`SUMMARY total=6 findings=0 skipped=0`,确认 `SHOW AFFINITY` 的 SQL 可见面来自 live InfoSchema,table/partition affinity 能随 rename/truncate/drop/drop database 正确表现,partition drop/remove partitioning 已由 affinity owner block;它是"外部 PD group + object-local SQL metadata + 既有 cleanup/block 覆盖"的降权样本。functional-index/hidden-column owner 筛选探针新增 5 格:`SUMMARY total=5 findings=0 skipped=0`,确认表达式索引引用列的 rename/change/drop 与语义 modify 以 `3837` block,`DROP INDEX` 后依赖解除,但同一条 multi-schema 里 `DROP INDEX + RENAME/DROP COLUMN` 仍按原始 schema block 且保留原 schema;这是 multi-schema 依赖消解边界样本。DB-level placement reference 探针新增 6 格:`SUMMARY total=6 findings=0 skipped=0`,确认 DB placement ref 可见、drop in-use policy 会被 `8241` block、`ALTER DATABASE` rewrite 后旧 policy 可 drop/新 policy 受保护、`DEFAULT` 和 `DROP DATABASE` 会释放 DB ref、旧表保留旧 DB default 而新表继承新 default。view reference 筛选探针新增 5 格:`SUMMARY total=5 findings=0 skipped=0`,确认 view 创建时校验 base table/column,但 metadata 存的是 SQL text;rename/drop base table/column 或跨库 drop base database 后 view 保留旧文本并变 invalid,这是名字绑定语义,不是必须 rewrite/block 的 DDL owner。resource-group `SWITCH_GROUP` 筛选探针新增 3 格:`SUMMARY total=3 findings=0 skipped=0`,确认 `CREATE RESOURCE GROUP` 允许 `SWITCH_GROUP` 指向 missing name,drop target 后 source 继续显示旧名字也只是未校验名字参数,不是已承诺维护的 object-identity ref。hypo-index side-metadata 探针新增 7 格:`SUMMARY total=7 findings=6 skipped=0`,红格是 session-local `USING HYPO` index 在 `RENAME/CHANGE/DROP COLUMN` 后仍引用旧/已删列,以及 `DROP TABLE`、`RENAME TABLE`、`DROP DATABASE` 后重建同名对象会复活旧 hypo index;草案见 `/Users/bba/pc/ai-native-hypo-index-reference-draft.md`。reorg/global-index 探针新增 2 格:`SUMMARY total=2 findings=1 skipped=0`,绿格确认 `REORGANIZE PARTITION` 会正确 rewrite partition placement refs;红格是 `REORGANIZE PARTITION p1` 成功后 replacement global index 漏掉后续 non-touched partition `pmax` 的行,`ADMIN CHECK TABLE` 报 `8223`,草案见 `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md`。add-index/global-index rollback delete-range 探针新增 3 格:红格是 partition table 上失败的 `ADD UNIQUE INDEX ... GLOBAL` rollback 后 `mysql.gc_delete_range` 只登记 partition-ID 前缀的 origin/temp index range,没有登记 table-ID 前缀的 global index range;绿格确认成功 `DROP GLOBAL INDEX` 用 table ID、local unique rollback 用 partition IDs。草案见 `/Users/bba/pc/ai-native-add-index-global-rollback-delete-range-draft.md`,方法 case 见 `/Users/bba/pc/ai-native-id30009-method-case.md`。当前必须按最新 add-index 新 bug 暂停门处理 id30009,不要继续扩 add-index 成功路径矩阵;reorg/global-index、hypo-index、sequence-default、masking-policy exchange 也仍保持暂停门。非 DDL pivot 侧已跑 3 个小矩阵/校准:普通 partition pruning 三路 oracle `ai_native_partition_prune_probe.py` findings=0;global-index × partition-pruning `ai_native_partition_global_index_prune_probe.py` findings=0;plan-cache parameter drift `ai_native_plan_cache_drift_probe.py` findings=0;随后用 InfoSchema custom extractor + CASE-wrapped oracle 命中 id30010。已降权:简单 table/partition/DB placement、view/resource-group 这种名字绑定或未校验名字参数、global-index/drop-truncate happy path、partition reorg rollback/retry 显式 failpoint 层、truncate transient failure 层、delete-range 普通入队层、已有 `putRuleBundlesError` 覆盖的 placement bundle failure 控制层、基础 FK table/index owner 层、masking-policy 基础 rewrite/cleanup 层(不含 id630014 exchange entrypoint)、privilege grant sys table 名字策略层、region split policy 这种嵌在同一对象元数据内的 SQL-visible 普通属性、affinity 这种 SQL-visible owner 从 live InfoSchema 枚举且外部 PD group 已有显式 cleanup/block 的路径、functional-index stale-reference/owner-removal 基础层(metadata-only MODIFY 不降权,id630007 已证明红格)、普通 partition pruning 三路等价、global-index partition pruning 常规过滤、以及当前参数矩阵覆盖到的 plan-cache drift 分支。delete-range GC worker 消费/redo 侧暂时也降权:外部自然调度最小 10 分钟,没有发现低成本 SQL/HTTP 触发入口。

**2026-07-13 新 high-severity 命中 id1590002: standalone IMPORT INTO sibling artifact commit ordering**。当前源码 `ImportSelectedRows` 先 `Close+Import` data engine,之后才 `Close+Import` index engine。旧 terminal-action screen 因 defer 会 close/cleanup 两个 engine 而把本函数退休,但新 proof dimension 发现 defer 不能回滚已导入 TiKV 的 record KVs,反而会清掉 index recovery state。testbed 8220955 上在 data import 成功后、index close 前注入错误:`IMPORT INTO` 返回 1105,普通 table scan=3,强制 secondary-index scan=0,`ADMIN CHECK TABLE` 报 8223;无故障控制为 3/3+ADMIN GREEN,故障上移到 data import 前为 0/0+ADMIN GREEN。新 selector `SIBLING_ARTIFACT_PRECOMMIT_ATOMICITY`:finalizer coverage 之后还必须证明 irreversible-boundary dominance 与 recovery-source retention。远端 `found_bug` 已确认为 `id1590002/high`,总计 92 行/69 个 root。target=`target.import-into-from-select.partial-data-before-index-finalization.v1`;草案 `docs/bug-drafts/ai-native-import-into-partial-data-before-index-finalization-draft.md`;方法 case `docs/method-cases/ai-native-import-into-partial-artifact-method-case.md`;资产 `assets/store/import-into-partial-artifact-results.jsonl`;探针 `scaffolds/top-level/ai_native_import_into_partial_artifact_probe.sh`。这是新 root,不是 id1260008 的 writer Close reachability,也不是旧 retired target 的重复执行。该 root 现在进入暂停门:不要枚举更多 Close/Import 错误类型;fix validation 必须覆盖 index Close 和 index Import 两个 post-data-import fault altitude。

**下一步(主线)**:
1. 当前 state-ingress 主线已有两个 pending-`tx_read_ts` 正例(binding-history、index advisor)、多个负样本(foreign-key、user-management、infoschema、BRIE、issyncer、importer job/precheck),一个 contract-blocked 样本(`SHOW TABLE STATUS`),以及两个从动态队列 pivot 出来的新 selector 正例:`USER_SESSION_STATE_RESTORE` 的 Fast `ADMIN CHECK TABLE`、`SYS_SESSION_POOLED_STATE_ISOLATION` 的 grant/revoke `Grantor` metadata。动态队列已排空,`store.py next` 为 `null`;`pooled-session-state` 与 `user-session-state-restore` 两个反哺规则也已跑出 0 新候选。下一步不要枚举 BATCH、partition 或泛扫 executor,而是切到另一个 oracle-debt/selector lane。只有能要求 AI 改 TiDB/harness 做错误注入或日志观测、并能证明 C3 用户后果的 target 才可进入主线。产品侧若要升级 binding-history/index-advisor/SHOW 边界样本,先确认 `SET TRANSACTION READ ONLY AS OF TIMESTAMP` 的 contract:它是给下一条用户读/execute,还是任意下一 statement 都能消费。
2. 非 DDL cache lane 的高价值命中是 id30034/id30035/id30036/id30037 的 S7 implicit-session-input cached payload:不要枚举所有日期函数或算术函数;下一步若继续 S7,应做 **`EvalContext`/`BuildContext` getter 级扫描**:列出 hidden session/config 输入,扣掉 plan-cache key 已覆盖和 execution-boundary 会重建的项,只对剩余"会被 constant folding/descriptor build 变成 cached payload"的函数/边界跑小矩阵。必备 oracle:direct session-variable contract + cache-hit RED + flush/off-cache reference + 非折叠控制 + 显式参数/执行期控制。当前已记录多个绿样本,包括 prepared DML `foreign_key_checks` 跟随当前开关且由 key/must-nil 保护,以及本轮 `GROUP_CONCAT` × `group_concat_max_len` cache-hit 仍正确。
3. `ERROR_IDENTITY_PRESERVATION` 的 terminal action lane 已命中 `dxf/importinto` 的 multi-writer Close bug。下一步不是继续泛扫所有 `Close()`,而是把本次 selector 固化成 source-target 规则:找"一个 owner Close/Flush/Commit 返回 root error 后,同一上层操作仍拥有 sibling terminal owner"的路径,且必须能加最小 observer/failpoint 证明 **root error preserved + sibling terminal action reached/forbidden**。优先 sibling 形状:multi-writer close、multi-engine cleanup、batch commit/abort、defer cleanup after first error。暂停门:如果只能看最终 error 文本,没有状态动作 observer,一律不执行;如果命中同一根因,只记录 blast-radius/负缓存,不要扩矩阵。
2. S8 guarded:id30028 confirmed + id30029 candidate 后,不要继续枚举 ordinary preprocessor/session switches。下一步只能做 id30029 的 contract/fix direction,或找一个**不同后果 oracle**的 S8 候选(例如非 DDL/DML correctness,不是 prepared DDL normalization),并且必须有 direct current-session reference + prepared switch matrix + flush/off-cache/uncacheable 控制。
3. id30031/id30030 的 S3 LIKE custom ESCAPE 已保持暂停门:第二个 owner 已证明 blast-radius,不要枚举 cluster_log pattern 或 InfoSchema pattern-matchable 列,只在另一个 omitted operator input 或不同 replacement mechanism 上重开。id30027/S3 cache key granularity 也保持暂停门:不要继续枚举所有 `InspectionTableCache` 用户。id30026/S3 type-domain conversion 不要继续随机枚举 `parseUint64` owner 或更多负数/字符串 ID 变体。id30025/S7 coarse-key sufficiency、id30024/S7 semantic-switch coverage 和 id30020/S7 Apply cache 保持暂停门。id30023/S3 request/render context split 不要继续扩更多 time column/timezone 变体。id30022/S3 backend-not-found 不要继续扩 `tikv_region_peers` 的 missing region-id 数值变体。id30021/S3 interval-overlap coarse skip 不要继续扩 `statements_summary` 的时间谓词排列。id30019/S3 helper family 也保持停止规则:不要继续枚举 `extractCol(..., valueToLower=true)` 的所有调用点;跨第二个 owner 已足够证明 blast radius。
4. id30009 add-index/global-index rollback delete-range 红格已从 candidate 提升为 confirmed,是当前最新 add-index 暂停门和 selector 来源:成功 drop global index 用 tableID,local rollback 用 partitionID,但失败/取消的 partition `ADD INDEX ... GLOBAL` rollback 会把 origin/temp global-index delete range 登到 partitionID,没有 tableID range。实锤证据:在 testbed `8192975` 上用 `create-index-stuck-before-public` 让 tableID=13190 的 global index 先写入 6 条 KV,随后 `ADMIN CANCEL DDL JOBS 13195`;rollback done 后 schema 已无索引,但 raw TiKV scan 在 tableID/indexID=1 下仍有 6 条 logical keys,登记的 partitionID origin/temp ranges 为空。最小复现、源码链路、fix direction 在 `/Users/bba/pc/ai-native-add-index-global-rollback-delete-range-draft.md`,方法验证 case 在 `/Users/bba/pc/ai-native-id30009-method-case.md`。不要继续扩 add-index 成功路径矩阵;已 CLOSED-FIXABLE(GUARDED 防 blast-radius),修复方向:rollback 重建 `ModifyIndexArgs` 时必须保留或重建 `IndexArg.IsGlobal`,否则 `delete_range.go` 会把 global index 当 local index 清理。
5. table-lock cross-schema rename 红格仍是暂停门和 selector 来源:同库 locked-table rename 是绿格,跨库 locked-table rename 后 `UNLOCK TABLES` 会留下新库表 stale lock。最小复现、源码链路、fix direction 在 `/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md`,方法验证 case 在 `/Users/bba/pc/ai-native-id30008-method-case.md`。不要继续扩更多 table-lock 变体;已 CLOSED-FIXABLE(GUARDED 防 blast-radius),修复方向:跨库 rename 要么同步 session lock entry 的 `SchemaID`,要么 unlock 按 `TableID` 反查当前 schema,不能继续相信旧 owner/container key。
6. reorg/global-index 红格仍是高质量暂停门和 selector 来源:2 格里 1 个 red,最小复现、源码链路、fix direction 和 fix-validation contract 在 `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md`,方法验证 case 在 `/Users/bba/pc/ai-native-id30007-method-case.md`。不要继续扩更多 reorg/global-index 变体;已 CLOSED-FIXABLE(GUARDED 防 blast-radius),修复方向:replacement global index 的 non-touched phase 必须覆盖 `pi.Definitions - pi.AddingDefinitions - pi.DroppingDefinitions`,不能在进入 non-touched phase 后又被 `AddingDefinitions` iterator 提前结束。修复验证应证明 middle/first/last/all-reorg 形状下的 set semantics,而不是只 replay 当前两行 repro。
7. hypo-index 红格仍是独立暂停门:7 格里 6 个 red,最小复现、源码链路和 fix-semantics 草案在 `/Users/bba/pc/ai-native-hypo-index-reference-draft.md`,方法验证 case 在 `/Users/bba/pc/ai-native-id30006-method-case.md`。不要继续扩更多 hypo 变体;下一步先讨论/确认修复语义:column rename/change/drop cleanup 或 rewrite,drop table/database cleanup,table rename rekey 或 cleanup,`SHOW CREATE TABLE` 做防御式过滤但不能替代 session map cleanup,同名对象重建不得继承旧 session side metadata。
8. sequence-default 红格仍是独立暂停门:最小复现、3 个红格、2 个绿格、源码链路和 fix-semantics 草案在 `/Users/bba/pc/ai-native-sequence-default-reference-draft.md`,方法验证 case 在 `/Users/bba/pc/ai-native-id30005-method-case.md`。不要继续扩更多 sequence 变体;下一步先讨论/确认修复语义:`DROP SEQUENCE` block,sequence rename block/rewrite,跨库 `DROP DATABASE` 删除被外部 table default 引用的 sequence 时 block。
9. stats column-rename 红格暂停门已完成到可讨论质量:最小复现、blast radius(`RENAME COLUMN`/`CHANGE COLUMN`)、预期语义、源码根因和 issue-ready body 都在 `/Users/bba/pc/ai-native-stats-column-rename-draft.md`。不要继续扩更多 stats 变体,除非要配合 owner 反馈或 fix 验证。
10. table-cache drop-database 红格暂停门已做到最小探针+源码链路+issue 草案:`/Users/bba/pc/ai-native-table-cache-drop-database-draft.md`。方法论案例已单独沉淀到 `/Users/bba/pc/ai-native-id30004-method-case.md`。下一步优先补充 owner 反馈需要的最小证据或修复方向,不要继续扩 table-cache 变体。窄源码校验后,当前更一致的修复语义倾向是 `DROP DATABASE` 遇到 cached table 直接 block;若 owner 选择允许 drop schema,则必须在 `ActionDropSchema` final state 清理所有 dropped table IDs 对应的 `mysql.table_cache_meta`。
11. 继续 DDL object-reference proof obligation 时,重新从源码/diff 找还没被专门测试覆盖的 DDL reference owner。筛选标准要更尖锐:除了继续找"side metadata/sys table/session cache + 多个 DDL 入口 + ID/name-keyed storage + name/API display + 低噪声 oracle"的 owner,还要新增 id30009 的 selector(成功路径 green,rollback sibling path 重建参数时丢失 owner/type bit,cleanup 后续分支信这个 bit)、id30007 的 selector(同一 owner 普通路径已绿,但 sibling path 有独立全量 iterator)和 id30008 的 selector(DDL-created side state 同时存 object ID 与 owner/container key,move/rekey path 只保留 object ID,cleanup path 仍信旧 owner key)。最新 next-owner scan 见 `/Users/bba/pc/ai-native-ddl-next-owner-scan.md`;已暂停 id30009 add-index/global-rollback delete-range、id30008 table-lock、id30007 reorg/global-index、id30006 hypo-index、id30005 sequence-default、id30003 stats family、id30004 table-cache。
12. 保持 oracle 纪律:旧引用是否解除、新引用是否受保护、错误族是否来自目标 owner、DDL 后验 `ADMIN CHECK`/index-vs-table rowset 是否一致、cleanup 后真实行为是否恢复。非 DDL shortcut 要额外坚持"普通 WHERE vs CASE-wrapped/显式 re-check/no-shortcut"等价;不要因为对象是 `information_schema` 就只看 plan。stats 这类 owner 要区分"设计上的 delayed GC"和"可见 API 的 stale reference";table-cache 这类 owner 要区分"block cached table"和"drop broader container must cleanup all table-ID side rows";hypo-index 这类 session side metadata 要区分"普通 session artifact"和"已被 SHOW CREATE 合并成 public DDL output 的 stale reference";global-index 这类数据 owner 要坚持 `USE INDEX` vs `IGNORE INDEX` + `ADMIN CHECK` 双 oracle;table-lock 这类 session/map owner 要坚持 unlock 后另一个 session 的行为 oracle。
13. id30001/id30002 只作为方法论证据保留;除非用户明确重开,不要继续 optimizer/executor proof family。

---

## 1. 新 session 开机步骤
```bash
export KUBECONFIG=/Users/bba/pc/tidb/kubeconfig.yml
# 若 testbed 过期/kubeconfig 失效:tcctl testbed get -p 8192975(会重写 /Users/bba/pc/kubeconfig.yml)
export KUBECONFIG=/Users/bba/pc/kubeconfig.yml
kubectl get pod fp-tidb                                    # 应 Running
kubectl get tc tc -o jsonpath='{.spec.tidb.replicas}'     # 应为 0(fp-tidb 当 owner)
# 重建 port-forward(笔记本休眠会断;先查 lsof -nP -iTCP:14000 -sTCP:LISTEN 是否还在)
nohup kubectl port-forward pod/fp-tidb 14000:4000 >/dev/null 2>&1 &
nohup kubectl port-forward pod/fp-tidb 18080:10080 >/dev/null 2>&1 &
sleep 4
# 健康检查
mysql -h127.0.0.1 -P14000 -uroot -e "SELECT VERSION();"    # 8192975 上 @@tidb_version 不存在
curl -s http://127.0.0.1:18080/fail/                      # 列已启用失败点
```
**⚠️ 若上个 session 被中断,可能残留 `pause` 失败点 hold 住 add-index job → 阻塞所有 DDL(CREATE TABLE 会 hang)。清理:**
```bash
for fp in afterWaitSchemaSynced beforeWaitSchemaSynced beforeAddIndexScan skipReorgWorkForTempIndex \
          mockDMLExecutionMerging create-index-stuck-before-public mockBackfillRunErr; do
  curl -s -X DELETE "http://127.0.0.1:18080/fail/github.com/pingcap/tidb/pkg/ddl/$fp" -o /dev/null -w "$fp=%{http_code} "; done; echo
mysql -h127.0.0.1 -P14000 -uroot -e "ADMIN SHOW DDL JOBS 5\G" | grep -iE 'JOB_ID|STATE|TABLE_NAME'
# 若有 running 卡住的 add index:ADMIN CANCEL DDL JOBS <id>;
mysql -h127.0.0.1 -P14000 -uroot -e "CREATE TABLE test.ping(a int); DROP TABLE test.ping;" && echo "DDL OK"
```

---

## 2. 集群状态(关键)
**2026-07-02 当前实际状态补充**:用户指定 testbed `8192975`;`tcctl testbed get -p 8192975` 写入 `/Users/bba/pc/kubeconfig.yml`;namespace 是 `testbed-tps-8192975-1-14`。managed TiDB 已为 0,`fp-tidb` 是唯一 TiDB + DDL owner。NodePort SQL `10.2.12.57:30386` 直连会卡住,当前可靠入口是 port-forward:`kubectl --kubeconfig=/Users/bba/pc/kubeconfig.yml port-forward pod/fp-tidb 14000:4000 18080:10080`。`SELECT VERSION()` 返回 `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`;`@@tidb_version` 不存在。id30007 reorg/global-index 探针已在该环境复现:`SUMMARY total=2 findings=1 skipped=0`,说明它不是之前 master testbed 独有信号。按用户要求,`tc.spec.tidb.config` 已加入 `enable-table-lock = true`,并且当前 `fp-tidb` 也以 `/tmp/fp-enable-table-lock.toml` 重启;`SHOW CONFIG WHERE NAME='enable-table-lock'` 返回 `true`。id30008 table-lock/cross-schema rename 已在该环境复现:session1 `LOCK/RENAME/UNLOCK` rc=0,session2 `INSERT` 报 `8020`,随后 `ADMIN CLEANUP TABLE LOCK` 和临时库清理成功。方法论新增要求:探针先记录 capability/version fingerprint,缺功能时 `SKIP(capability)`,不要把环境差异当产品 bug。

**2026-07-01 当前实际状态补充**:当前 testbed 是 `8194177`;failpoint-enabled `fp-tidb` 已重建并启动,managed TiDB 已缩到 `tc.spec.tidb.replicas=0`,`fp-tidb` 是唯一 TiDB + DDL owner。端口转发用 `kubectl port-forward pod/fp-tidb 14000:4000 18080:10080`。已用它跑通 stateful object-reference 探针 14 格。

- **testbed 8194177**;`export KUBECONFIG=/Users/bba/pc/tidb/kubeconfig.yml`。集群 **v9.0.0-beta master,commit `5c9198e948`,build 2026-06-22**,Kernel=Classic,store=tikv(fp-tidb owner + 3 TiKV,k8s)。
- **managed tidb 已缩到 0**(`tc.spec.tidb.replicas=0`),**`fp-tidb` pod 是唯一 tidb + DDL owner**(failpoint 编译版,与集群同 commit)。owner pod IP 历史值 `10.200.30.115:4000`(重建 pod 会变)。
- **访问只能走 port-forward**(因为 managed=0,tc-tidb NodePort `10.2.12.57:30386` 已无后端):
  - SQL:`mysql -h127.0.0.1 -P14000 -uroot`(14000→fp-tidb:4000)
  - 失败点/状态口:`http://127.0.0.1:18080`(18080→fp-tidb:10080)
- **fp-tidb pod 内**:`/fp`=failpoint tidb 二进制;`/ddlfuzz`=随机 fuzzer(已弃);pod PID1 是 `sleep infinity`,所以 tidb panic 只死子进程、pod 不重启(**panic 检测 = 查 `/fp` 进程是否还在**)。
- **失败点动态控制**(靠启动时 `GO_FAILPOINTS=...pkg/server/enableTestAPI=return(true)` 挂出 `/fail/`):
  - 激活 `curl -X PUT -d '<action>' http://127.0.0.1:18080/fail/<失败点全名>`(全名=包导入路径/名,如 `github.com/pingcap/tidb/pkg/ddl/beforeAddIndexScan`)
  - 读 `curl .../fail/<全名>`;列表 `curl .../fail/`;清除 `curl -X DELETE .../fail/<全名>`
  - action:`return(true)`、`return("str")`、`1*return(true)`(一次)、`pause`(阻塞直到 DELETE)
- **复原集群**(用完):`kubectl patch tc tc --type merge -p '{"spec":{"tidb":{"replicas":1}}}'`;`kubectl delete pod fp-tidb`。

---

## 3. 方法论(项目的核心论点)
**总闭环**:历史 bug 入库 → AI 挖「场景 + 不变量」→ 编译成「生成器 + 可执行 oracle」→ 廉价引擎跑量执行 → 命中即最小化+triage → 反哺 bug 库。

**几个被反复验证的关键洞察**:
1. **detection 不是瓶颈,瓶颈在生成/导向**。mutation checker + assertion + ADMIN CHECK 这些 oracle 已现成且默认开;难的是把算力送到脆弱代码。**效率公式 = 目标密度 × 触发确定性 × oracle 灵敏度**。
2. **随机/盲目 fuzz 在 hardened master 上低效**(schrddl 30min、ddlfuzz 4h 均 0 真命中)。99% 算力碰不到脆弱代码。→ 必须**定向**。
3. **生成空间宽、oracle 空间极小**:102 个场景的多样 trigger,最终 invariant_kind 收敛到 ~7 个 oracle 家族(liveness / consistency / correctness / robustness / no-panic / metadata / atomicity);add-index 一致性 37 个不同 trigger → 基本 1 个主导 oracle(`ADMIN CHECK` + 索引/全表行集差分)全覆盖。**这是杠杆:挖 oracle 一次,覆盖一大片生成空间。**
4. **静默正确性 bug 是主战场**:历史 bug 里 ~26% 是数据不一致/错结果(无崩溃),只有不变量 oracle 抓得到;算上控制/元数据/挂死,~84% 纯崩溃 fuzz 看不见。这是"AI 挖 oracle"相对随机 fuzz 最硬的优越性。
5. **`inject_point` 是挖掘→生成的桥**:82 子场景里 67(82%) 带 `inject_point` 维度(reorg 子状态 DeleteOnly/WriteOnly/WriteReorg/scan/before-import/before-merge/ReadyToMerge),直接对应 TiDB 真实 failpoint 名 → 用 `pause` 钉死子状态来确定性制造罕见交错,不靠随机时序撞。
6. **AI 写 fuzz、不写单 case**:AI 当编译器+元控制器(挖→编译生成器/oracle/权重、命中后 triage/最小化、轮次间重调),廉价引擎跑量。AI 成本 = O(战役数) 而非 O(执行数)。三个坎:oracle 误报会被走量放大(需自校验门)、静态会见顶(AI 要留元循环)、要接覆盖率反馈。
7. **种子变异 > 随机生成**:bug 库 234 个回归测试 = 真崩过的精确配方;在其旁边小扰动(换列类型/加并发/换 failpoint/拼接/搬新特性)命中相邻新 bug(尤其 incomplete-fix 类)概率远高于随机。配合 **diff-directed**(优先变异最近改动文件附近的种子)。
8. **shortcut/extractor 也要按证明义务打**:id30010 说明非 DDL 模块不应退回随机 SQL。高效路径是先从源码找到自定义 shortcut/cache/extractor 正在替 SQL 语义做的承诺,再构造小 adversarial name/value set,用 CASE-wrapped、显式 re-check、no-shortcut/safe-path 等价 oracle 验证。系统表并非天然不可测;关键是 oracle 不能只看 plan,必须证明 SQL-visible 谓词本身和 shortcut 返回行集一致。
9. **cache/reuse 要拆成 getter 与 payload 证明**:key completeness(哪些输入决定可复用)、payload purity(缓存的对象是否包含不可复用结果)、semantic-switch coverage(哪些 session/config 变量在 cached object 构造期改变语义)、coarse-key sufficiency(近似 key 是否覆盖所有 cached/folded 语义)、payload-class mapping(同一个 hidden input 会写入 folded scalar、semantic tree、range/request boundary、type/descriptor 等不同载体)。id30020 命中 payload purity,id30024 命中 semantic-switch coverage,id30025 命中 coarse-key sufficiency,id30034/id30035 命中 implicit-input folded scalar,id30036 命中 implicit-input aggregate/type descriptor,id30037 命中 implicit-input literal collation metadata;后续不要随机扫缓存,要先证明“代码检查了 P、系统相信 Q、因此跳过 safe path”的具体链条。

设计文档(飞书)已写入:「生成场景和约束」「执行:让 AI 构建并持续调教 Fuzz」「进阶方法与创新点」三节,顶部 Key Idea/目标/Milestone1 已对齐。

---

## 4. 资产
- **Bug 库**(TiDB Cloud serverless):`mysql --login-path=tidbbug --ssl-mode=VERIFY_IDENTITY --ssl-ca=/etc/ssl/cert.pem -D test`(密码在 `~/.mylogin.cnf`,不在对话里)。
  - `bug_ddl`:**451 个真实历史 DDL bug**(closed:critical 83 + major 349 + schrddl 额外 19;字段 title/bug_type/fix_pr/has_regression_test/repro/violated_invariant/scenario_dims(JSON)/severity/affects/issue_created_at)。329 带 fix_pr、234 带回归测试。
  - `v_bug`:派生视图(+ ddl_family / has_concurrent / has_fault / is_ingest)。
  - `ddl_scenario`:**102 条目录** = 20 伞状(A1-A7/B1/C1-5/G1-4/I1-3)+ 82 挖掘子场景(A1.1-3 / A4.1-7 / A5.1-12 / A6.1-8+D1-3 / A8.1-5 metamorphic / A9.1-8 元数据 / C1.1-C5.6 / I1.1-I3.4)。每条带 `scenario_dims`(含 `inject_point`)、`invariant`、`oracle`、`example_ids`(回指真实 bug)。回测覆盖三族 274/290(94%;未覆盖的 16 全是 perf/OOM/test-infra 等误标,扣掉后 in-scope ≈100%)。
- `found_bug`:本项目方法论新挖出的 bug。历史 confirmed/candidate 摘要(长清单截至 id30023;最新 id30024-id30032/id630001-id630014 见下一条): id1 CHECK 约束改名静默丢(中危),id2 partial-index RENAME 误 1054(低危),id30001 partial-index 蕴含判断导致 SELECT wrong-result(高危),id30002 candidate predicate-simplification/collation wrong-result(方法论旁证,不作为当前 DDL 主线),id30003 stats column-rename candidate(`SHOW STATS_HISTOGRAMS` stale column name after DDL rename/change),id30004 table-cache drop-database candidate(`DROP DATABASE` cached table leaves `mysql.table_cache_meta` orphan table ID),id30005 sequence-default reference candidate(sequence drop/rename/drop-database leaves dangling default),id30006 hypo-index session metadata candidate(`USING HYPO` index survives column/table/database DDL and appears stale/resurrected in `SHOW CREATE TABLE`),id30007 reorg-partition global-index candidate(`REORGANIZE PARTITION` succeeds但 replacement global index misses rows from later non-touched partitions, `ADMIN CHECK TABLE` reports `8223`),id30008 table-lock cross-schema rename confirmed(`UNLOCK TABLES` succeeds after `RENAME TABLE src.t TO dst.t`,但 `dst.t` 仍被其他 session 视为 locked;testbed 8192975 已复现),id30009 add-index/global-index rollback delete-range confirmed(partition `ADD INDEX ... GLOBAL` rollback registers cleanup ranges under partition IDs instead of table ID and leaves tableID global-index KV orphaned),id30010 InfoSchema LIKE extractor confirmed(`information_schema.tables/columns.TABLE_NAME LIKE 'a_%'` under `utf8mb4_bin` returns mixed-case `Acase` that SQL-visible predicate evaluates false;已写入 `found_bug`, `confirmed=1,status=confirmed`),id30011 FLASHBACK DATABASE dangling placement ref confirmed(`onRecoverSchema` 原样写回 DBInfo,恢复库引用已删 policy,`CREATE TABLE` 报 8239;methodology v2 首个命中,6 格矩阵 2 红格;`confirmed=1,status=confirmed`),id30012 cluster_log time extractor confirmed(`ClusterLogTableExtractor` 用 `time.Local` 而非 session tz,非 UTC session 下时间过滤 wrong-result;S3 selector 第二次命中,源码优先、双向差分;`confirmed=1,status=confirmed`),id30013 cluster diagnostic `type` collation candidate(`extractCol(valueToLower=true)` over bin-collation enum;已入库 `candidate`),id30014 ANALYZE lifecycle confirmed(中断 partitioned ANALYZE 后 stale running analyze job 引用 dead process_id;已入库),id30015 cluster_log sub-ms time equality confirmed(DATETIME(6) literal 被截成毫秒后端窗口且无 scalar recheck,返回行自身谓词为 false;已入库),id30016 FK FLASHBACK TABLE missing parent confirmed(`FLASHBACK TABLE` 恢复子表时发布缺父表 FK,父表缺失期间 orphan insert 成功;已入库),id30017 stats lock exchange-partition confirmed(`LOCK STATS t` 经 `EXCHANGE PARTITION` 后 `UNLOCK STATS t` 留下 exchanged table `t1` locked;已入库),id30018 InfoSchema scalar-pushdown normalization confirmed(`LOWER/UPPER(TABLE_NAME)` wrong-case const 返回自谓词为 false 的 `Acase`;已入库),id30019 metrics_summary name normalization confirmed(`METRICS_NAME='TIDB_QPS'` 返回自谓词为 false 的 `tidb_qps`;已入库,作为 `valueToLower=true` helper 跨 owner 代表 blast-radius case),id30020 Apply cache volatile subquery confirmed(duplicate correlated key 下缓存复用 `UUID()` inner scalar subquery 结果,cache ON distinct UUID 从 24/16 塌到 1/1,cache OFF 恢复;已入库,作为 S7 cache payload purity 首个命中),id30021 statements_summary coarse interval skip confirmed(`summary_begin_time <= A AND summary_end_time >= B` 在 A<B 且窗口覆盖时被 `skip_request:true` 错误返回 0,CASE reference 有 34+ 行;已入库,作为 S3 interval-overlap coarse skip 子形状),id30022 tikv_region_peers backend not-found confirmed(`region_id=0` / `region_id IN (0,2)` fast path 返回 PD error,CASE reference 分别为空/valid rows;已入库,作为 S3 backend error-domain 子形状),id30023 tidb_hot_regions_history timezone render confirmed(`+14:00` 时间窗 fast path 返回自谓词为 false 的 UTC 显示行,CASE reference 为空;已入库,作为 S3 request/render context split 子形状)。
- `found_bug` 最新远端校验:2026-07-03 已到 `MAX(id)=1020001,COUNT=68,COUNT(DISTINCT root_cause_id)=46`。最新插入 row 是 id630025。
  - id630025=`EXCHANGE PARTITION WITH VALIDATION fails on LIST DEFAULT partitions due to invalid internal check SQL` confirmed,oracle=`O24_PARTITION_EXCHANGE_VALIDATION_ORACLE`,method=`S19_VALIDATION_SQL_BUILDER_GAP`,root_cause_id=`exchange-default-validation-sql`。
  - id30039=`EXCHANGE PARTITION can leak saved ANALYZE options to the exchanged standalone table` confirmed,oracle=`O21_SIDE_STATE_OWNER_REMAP`,method=`S4_STALE_OWNER_CONTAINER_KEY`,root_cause_id=`exchange-idswap-orphan`。
  - id30038=`ADD UNIQUE INDEX can mis-detect duplicates for a multi-valued index when added with a multi-column index under concurrent DML` confirmed,oracle=`O22_BACKFILL_CONCURRENT_DML_DIFFERENTIAL`,method=`S1_FLATTENED_KEY_OWNER_MAPPING`。
  - id630024=`EXCHANGE PARTITION leaves stale TTL status after swapping a TTL table ID` confirmed,oracle=`O21_SIDE_STATE_OWNER_REMAP_ORACLE`,method=`S4_ID_SWAP_OWNER_MAPPING`,root_cause_id=`exchange-idswap-orphan`。
  - id630023=`MODIFY COLUMN rejects adding NOT NULL to partition columns with no NULL rows` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S10_DDL_VALIDATION_METRIC_MISMATCH`。
  - id1020001=`CREATE USER IF NOT EXISTS can still fail while validating unused PASSWORD EXPIRE for anonymous user` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630022=`CREATE SPATIAL INDEX IF NOT EXISTS can still fail before duplicate index no-op` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630021=`CREATE MASKING POLICY IF NOT EXISTS can still fail while validating unused masking expressions` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630020=`CREATE RESOURCE GROUP IF NOT EXISTS can still fail while building unused BACKGROUND options` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630019=`CREATE SEQUENCE IF NOT EXISTS can still fail while validating unused sequence options` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630018=`CREATE TABLE IF NOT EXISTS can still fail while validating unused table definitions` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630017=`DROP INDEX IF EXISTS PRIMARY still errors when no primary key exists` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id30037=`Prepared plan cache reuses _utf8mb4 literal collation after default_collation_for_utf8mb4 changes` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_IMPLICIT_SESSION_INPUT_LITERAL_COLLATION`。
  - id30036=`Prepared plan cache reuses AVG decimal scale after div_precision_increment changes` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_IMPLICIT_SESSION_INPUT_AGG_PAYLOAD`。
  - id30035=`Prepared plan cache reuses division constants after div_precision_increment changes` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_IMPLICIT_SESSION_INPUT_CONSTANT_FOLD`。
  - id30034=`Prepared plan cache reuses WEEK() constants after default_week_format changes` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_IMPLICIT_SESSION_INPUT_CONSTANT_FOLD`。
  - id630016=`ADD PARTITION IF NOT EXISTS can still error on existing partition when LIST table has DEFAULT partition` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id630015=`DROP PARTITION IF EXISTS can still error when missing names look like all partitions` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING`。
  - id30032=`ALTER TABLE ADD COLUMN silently drops inline CHECK constraints` confirmed,oracle=`O23_TARGET_SCHEMA_CONSTRAINT_REFERENCE`,method=`S18_EMBEDDED_CONSTRAINT_OWNER_LOSS`。
  - id630013=`MODIFY COLUMN can leave rows violating existing CHECK constraints` confirmed,oracle=`O20_POST_CONVERSION_CHECK_ORACLE`,method=`S17_DDL_REORG_CONSTRAINT_BYPASS`。
  - id630012=`MODIFY COLUMN can make FK child signedness incompatible with parent` confirmed,oracle=`O19_TARGET_STATE_REJECTION_REFERENCE`,method=`S16_DDL_VALIDATOR_ORDERING_GAP`。
  - id630011=`MODIFY COLUMN allows NOT NULL child column for foreign key SET NULL actions` confirmed,oracle=`O19_TARGET_STATE_REJECTION_REFERENCE`,method=`S16_DDL_VALIDATOR_ORDERING_GAP`。
  - id630010=`ADD IF NOT EXISTS table-element list still errors on existing indexes/check constraints` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_FLAG_DROPPED`。
  - id630009=`MODIFY COLUMN rejects metadata-only changes on columns used by partial-index conditions` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S11_DDL_DEPENDENCY_GATE_OVERBROAD`。
  - id630008=`ADD FOREIGN KEY IF NOT EXISTS still errors on existing foreign key` confirmed,oracle=`O18_IDEMPOTENT_DDL_FLAG_ORACLE`,method=`S15_DDL_IDEMPOTENCE_FLAG_DROPPED`。
  - id630007=`MODIFY COLUMN rejects metadata-only changes on columns used by expression indexes` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S11_DDL_DEPENDENCY_GATE_OVERBROAD`;companion/blast-radius of id630004,not a new root-cause family。
  - id630006=`FLASHBACK TABLE can restore duplicate CHECK constraint names in one schema` confirmed,oracle=`O17_SCHEMA_CHECK_CONSTRAINT_NAMESPACE_ORACLE`,method=`S14_DDL_RECOVERY_NAMESPACE_VALIDATION_BYPASS`。
  - id630005=`CREATE TABLE LIKE mutates source CHECK constraint names in SHOW CREATE TABLE` confirmed,oracle=`O16_SOURCE_TARGET_METADATA_ISOLATION_ORACLE`,method=`S13_DDL_SHALLOW_COPY_TARGET_MUTATION`。
  - id630004=`MODIFY COLUMN rejects metadata-only changes on columns used by generated columns` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S11_DDL_DEPENDENCY_GATE_OVERBROAD`;id630003=`MODIFY COLUMN rejects safe VARCHAR shrink on partition columns` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S10_DDL_VALIDATION_METRIC_MISMATCH`;id630002=`MODIFY COLUMN rejects foreign-key VARCHAR length changes that target schema accepts` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S10_DDL_VALIDATION_METRIC_MISMATCH`;id630001=`MODIFY COLUMN shrink rejects valid multibyte CHAR/VARCHAR values` confirmed,oracle=`O14_TARGET_TYPE_ACCEPTANCE_REFERENCE`,method=`S10_DDL_PRECHECK_METRIC_MISMATCH`;id600001=`REORGANIZE PARTITION silently drops duplicate nonclustered rows after EXCHANGE PARTITION` confirmed,oracle=`O13_ROWSET_CARDINALITY_INVARIANT`,method=`S9_REORG_BACKFILL_IDENTITY_FAST_PATH`;id30031=`information_schema LIKE with custom ESCAPE can return rows that fail the predicate` confirmed,oracle=`O4_CASE_SELF_PREDICATE`,method=`S3_OPERATOR_SEMANTIC_ARITY`;id30030=`cluster_log LIKE with custom ESCAPE can drop matching log rows` confirmed,oracle=`O4_SCALAR_RECHECK_DIFFERENTIAL`,method=`S3_SHORTCUT_EXTRACTOR_LOSSY_PREFILTER`;id30029=`prepared CREATE TABLE freezes non-strict VARCHAR auto-conversion across later strict sql_mode` candidate,oracle=`O12_DIRECT_VS_PREPARED_REFERENCE`,method=`S8_PREPARED_PREPROCESS_SEMANTIC_FREEZE`;id30028=`prepared statements bypass tidb_enable_noop_functions after the switch is turned off` confirmed,oracle=`O12_DIRECT_VS_PREPARED_REFERENCE`,method=`S8_PREPARED_PREPROCESS_SEMANTIC_FREEZE`;id30027=`inspection_result config details leak cached cluster_config rows across component types` confirmed,oracle=`O4_NO_SHORTCUT_REFERENCE`,method=`S3_CACHE_KEY_GRANULARITY`;id30026=`tikv_region_peers drops negative region_id/store_id predicates and returns all peers` confirmed,oracle=`O4_CASE_SELF_PREDICATE`,method=`S3_TYPE_DOMAIN_CONVERSION`;id30025=`prepared plan cache reuses timezone-folded UNIX_TIMESTAMP literals across zones with same current offset` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_COARSE_TIMEZONE_KEY`;id30024=`prepared plan cache ignores tidb_sysdate_is_now semantic switch` confirmed,oracle=`O11_CACHE_HIT_FLUSH_REFERENCE`,method=`S7_SEMANTIC_SWITCH_KEY`。
- **设计文档**(飞书):`lark-cli docs +fetch/+update --doc O1SIdkuKfoBvFvxrebqcw83En9h`。
- **代码/二进制**(scratchpad,绝对路径,磁盘持久):
  `/private/tmp/claude-501/-Users-bba-pc-tidb/1567df2a-68fa-435c-9d24-23a65f59a2cf/scratchpad/fp-build/`
  - = `git worktree add --detach <dir> 5c9198e948` + `make failpoint-enable`(`git worktree list` 在 ~/pc/tidb 可见)。grep failpoint 用法/找种子配方就在这棵 worktree(与运行二进制同 commit)。
  - `tidb-server-fp`:linux/amd64 failpoint tidb(已部署进 pod)。
  - `cmd/ddlfuzz/main.go` + `ddlfuzz`:随机 fuzzer(已弃)。`mined/extract.py`、`consolidate.py`:挖掘抽取/入库脚本。`hunt.py`、`a1_harness.sh`:早期本机版(已弃)。
- **schrddl**:`~/pc/schrddl/schrddl`(prebuilt 146MB)= 设计文档要复用的执行引擎(见 §5)。
- **proof-obligation 目录**:`/Users/bba/pc/ai-native-proof-obligation-catalog.md`。当前 P0 节奏仍是 DDL-only reference/validator ownership matrix;reorg-partition global-index、reorg duplicate-rowid repair、stats column-rename、table-cache drop-database、sequence-default dangling reference、hypo-index session metadata 都已进入暂停门/可讨论质量。
  - 最新 DDL proof:id630013 证明 DDL reorg writer 也有 row invariant 证明义务,`MODIFY COLUMN` 不能只证明 old row 满足 CHECK 和 cast 成功,还必须证明 converted row 满足 CHECK;id630012 证明 coarse `type/flen/decimal` 相等不能替代完整 FK target-state compatibility,`INT`->`INT UNSIGNED` 会绕过 MODIFY FK 校验并在 cascade 负数时 fail-stop;id630011 证明 DDL validator 必须在完整 target state 上运行,不能先校验旧/半成品列状态再应用 `NOT NULL` 等影响兼容性的 option;id630010 证明 parser/spec split 也可能丢 idempotence flag;id630008 证明 parser/AST 接受的 idempotence flag 必须被每个 sibling DDL owner 传递到执行分支;id630006 证明 recovery/flashback 路径不能绕过 create/add 已有的 schema-level namespace validator。
  - DDL modify/reconstruct proof:id630001 证明 no-reorg precheck metric 必须匹配目标类型契约;id630002 证明 FK target-state validator 不能比 CREATE/ADD FOREIGN KEY 多套隐藏长度不等式;id630003 证明 coarse transition allowlist 不能在 target partition-definition/data-fit validator 前拒绝 safe final schema;id630023 证明 partition-column flag/nullability allowlist 也不能在通用 NULL data-fit check 前拒绝 safe final schema;id630004/id630007 证明 dependency existence 不能替代 semantic-change proof;id630005 证明 target reconstruction 不能浅拷贝源 metadata 后原地 mutate pointer-backed fields;id600001 证明 target-key/raw-byte equality 不能替代 source physical partition identity。
  - 非 DDL proof:id30010/id30018/id30019 已进入 extractor/name-normalization 暂停门;id30020 是 S7 cache payload purity 首个命中;id30021 证明 interval rows 不能按 point range 的 `start>end` 直接 skip;id30022 证明 backend object-not-found 不能直接等价为 SQL filter failure;id30023 证明 backend request context 与 SQL-visible row render context 必须一致;id30024/id30025 证明 cached object key 必须覆盖 semantic switch/timezone-folded value;id30026/id30027 证明 shortcut/request domain 的 consumed dimensions 必须保真;id30028/id30029 证明 PREPARE-time validator/AST mutation 不能在 EXECUTE 时盲信旧 session 语义;id30030/id30031 证明替代 `LIKE` scalar operator 时不能只保存 pattern string,`ESCAPE` 也是语义输入,且第二个 extractor owner 命中后必须停止枚举 helper 用户;id30034/id30035/id30036/id30037 证明 S7 里 implicit session input 不是函数名问题,而是 cached payload class 问题:folded scalar value、literal type/collation 与 aggregate/type descriptor 都可能 stale。
  - 负样本校准:privilege grant sys tables、region split policy、affinity、DB-level placement、view、resource-group `SWITCH_GROUP`、hypo TiFlash、SQL binding、local temporary table 已作为负样本;functional-index 只把 stale-reference/owner-removal 基础矩阵作为负样本,metadata-only MODIFY 已由 id630007 证明为红格。id30001/id30002 仅作为方法论证据保留,不继续 optimizer/executor proof family。
- **partition/pruning/plan-cache 探针**:`/Users/bba/pc/ai_native_partition_prune_probe.py` 比较未分区参考表、static pruning、dynamic pruning 三条路径的有序行集;`/Users/bba/pc/ai_native_partition_global_index_prune_probe.py` 覆盖 global-index × partition-pruning 小矩阵;`/Users/bba/pc/ai_native_plan_cache_drift_probe.py` 覆盖 prepared cache vs nocache vs direct literal 三路 oracle。
- **项目记忆**:`~/.claude/projects/-Users-bba-pc-tidb/memory/ai-native-test-framework.md`。

---

## 5. 已完成与验证
1. **Bug 库 + 目录**(2026-06-22/28/29):451 bug 入库;归一化;挖出 102 条 `(场景→不变量→oracle)`,回测三族 274/290。详见 §4。
2. **schrddl demo**(机制验证):并发 DDL+DML + `ADMIN CHECK`(G3/A1)+ metamorphic with/without-hint(G4)+ 活性超时(G2/A5);TLP/NoREC 查询正确性引擎。hardened master 30min **0 真命中**;唯一 TLP 命中是 `information_schema` 误报 → 坐实 **triage 门:只信稳定用户表上的差异**。跑法:`~/pc/schrddl/schrddl -addr H:P -db D -mode parallel -concurrency 15 -tables 3 -time 25m -logtostderr`。
3. **failpoint TiDB 部署**(2026-06-29):worktree 检出同 commit → `make failpoint-enable` → `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tidb-server-fp ./cmd/tidb-server` → pod 无 tar 用 `kubectl exec -i fp-tidb -- sh -c 'busybox gunzip -c >/fp && chmod +x /fp' <tidb-server-fp.gz` 流式塞入 → `GO_FAILPOINTS=...enableTestAPI=return(true) setsid /fp --store=tikv --path=tc-pd:2379 --host=0.0.0.0 -P=4000 --status=10080 --advertise-address=<podIP>` → 缩 managed=0 让 fp-tidb 当 owner。已验证 `create-index-stuck-before-public=return("/tmp/sig")` 能 hold(`kubectl exec fp-tidb -- touch /tmp/sig` 放行)。
4. **ddlfuzz 随机 fuzzer**(2026-06-29):13 类 exotic × fast_reorg × 并发 DML × 故障注入 × 双 oracle,pod 内 4h 长跑 → **0 命中**(印证随机生成低效)。→ 用户据此要求转向**高效/种子**方法论。
5. **LINCHPIN 验证 = YES**(2026-06-29):`curl PUT -d pause .../afterWaitSchemaSynced` 把 add-index job 稳稳 hold 在 schema 子状态(空表 hold 在 delete-only,done=N×5×2s),DELETE 后立即完成。**→ InjectCall 型失败点的 pause 可从外部驱动,种子引擎外部化路线打通。**
6. **TRUE-positive demo = 已坐实**(2026-06-29,见 §7)。
7. **partial-index correctness 真命中**(2026-06-30):真 TiKV 集群上用 `USE/IGNORE INDEX` 行集差分 oracle 命中 found_bug id30001。最小复现见 §7d;探针脚本 `/Users/bba/pc/ai_native_partial_index_probe.py`。
8. **DDL object-reference lane**(2026-07-01/02):从 column-reference 扩到 table/index/partition/sys-table/session side metadata,累计确认 stats、table-cache、sequence-default、hypo-index、reorg/global-index、table-lock、add-index/global rollback delete-range 等暂停门;一批 owner 被负样本降权,避免继续盲扩。
9. **非 DDL shortcut/extractor/cache lane**(2026-07-02/03):普通 partition pruning、global-index partition pruning、plan-cache drift 三个小矩阵均 `findings=0`;随后用 InfoSchema predicate extractor + CASE-wrapped/显式 re-check oracle 命中并入库 id30010,再用 scalar-pushdown + value-normalization + row self-predicate oracle 命中并入库 id30018,最后用同一 helper 的第二 owner `metrics_summary` 入库 id30019。按 id30019 的停止规则,没有继续枚举 helper 用户,而是切到 Apply cache 的新证明义务,用 cache ON/OFF + UUID volatile oracle 命中 id30020;随后又切到 statements_summary coarse interval skip,用 `skip_request:true` + CASE reference 命中 id30021;再切到 backend point-lookup error-domain,用 `region_ids:[...]` + CASE reference 命中 id30022;再切到 request/render context split,用 `tidb_hot_regions_history` 非 UTC 时间窗 + row self-predicate + CASE reference 命中 id30023;再从同一个 extractor 框架的 numeric conversion 证明义务出发,用 `region_id/store_id=-1` + CASE/self-predicate oracle 命中 id30026;再切到 inspection table cache key granularity,用 direct `cluster_config` reference vs `inspection_result` detail 命中 id30027。方法结论是"切模块可以,但仍然必须从源码 proof obligation 出发";generic helper 跨第二 owner 命中后要停在代表性 blast-radius,cache/reuse fast path 要额外证明 cached payload purity 和 cache key granularity,interval/window row 的 shortcut skip 要证明原 SQL 谓词不可满足,backend lookup fast path 要证明 object-not-found 是否应映射为 SQL 空结果,backend request context 和 row render context 要一致,SQL 值到 backend request domain 的转换也必须保真。
10. **S7 semantic-switch refinement**(2026-07-03):沿 Apply-cache 的 cache/reuse 证明义务继续,不是随机扩 executor。读 `NewPlanCacheKey` 和 `sysdate()` 构造链路后,发现 `tidb_sysdate_is_now` 会改变 scalar-function 语义但不在 prepared plan cache key 中;用 `@@last_plan_from_cache` + `ADMIN FLUSH SESSION PLAN_CACHE` 双 oracle 命中并入库 id30024。方法结论:S7 现在分三类证明,即 key completeness、payload purity、semantic-switch coverage。
11. **S7 coarse-key sufficiency refinement**(2026-07-03):没有把 timezone green 校准丢掉,而是追问同一个 coarse key 在哪个语义边界不会 rebuild。`NewPlanCacheKey` 只存当前 timezone offset;TIMESTAMP range hit 后会按当前 timezone rebuild,所以 green;但 `UNIX_TIMESTAMP(datetime literal)` 会在构造期折叠,所以 Johannesburg/Amsterdam 当前 offset 相同但历史 offset 不同时 cache hit 复用旧常量。用双向 timezone 切换 + same-statement flush reference 命中并入库 id30025。方法结论:S7 现在分四类证明,加入 coarse-key sufficiency。
12. **S3 type-domain conversion refinement**(2026-07-03):在 S7 暂停门后切回 shortcut/extractor,不是继续枚举 timezone。读 `memtable_predicate_extractor.go` 后发现 `extractCol` 删除 SQL 谓词,但 `TikvRegionPeersExtractor` 随后用 `parseUint64` 将字符串集合转 backend ID;`parseUint64` 对 `-1` 这类 out-of-domain 值静默忽略。testbed 上 `region_id=-1`、`store_id=-1`、`region_id IN (-1)` 都返回全表 269 行,CASE reference 为 0,返回行自身谓词为 0;`peer_id=-1` 绿色控制为 0/0,混合 `IN(-1, valid)` 只返回 valid 行。已入库 id30026。方法结论:S3 新增 type-domain conversion:extractor 不能只证明"谓词可提取",还要证明转换成 backend request domain 的结果与 SQL 谓词等价。
13. **S3 cache key granularity refinement**(2026-07-03):遵守 id30026 暂停门,没有继续枚举 `parseUint64` owner,而是切到 inspection memtable cache。源码证明义务是 `InspectionTableCache` 只按表名缓存,但后续 `cluster_config` detail query 的 `type='tikv'` 会被 extractor 消费成 `node_types:["tikv"]` 并从 scalar Selection 删除。testbed 上 direct `cluster_config WHERE type='tikv' AND key='foo-test'` 只有 `tikv-a,tikv-b`,但 `inspection_result` 的 `type='tikv'` config detail 包含 `tidb-a`;`@@warning_count=0`。已入库 id30027。方法结论:S3 新增 cache key granularity:cache fast path 不仅要证明 payload 可复用,还要证明 cache key 覆盖被 extractor 消费掉的语义维度,否则必须 recheck 或禁用该 cache hit。
14. **S8 prepared/preprocess semantic freeze**(2026-07-03):遵守 id30027 暂停门,没有继续枚举 inspection cache 用户,而是切到 prepared statement 的 PREPARE-time validation。源码证明义务是 `GeneratePlanCacheStmtWithAST` 在 PREPARE 阶段跑 `Preprocess`,而 `checkSelectNoopFuncs`/`checkGroupBy` 读取 `tidb_enable_noop_functions`;EXECUTE 只在 schema version 变化时重跑 Preprocess。testbed 上 direct `OFF` 会拒绝 `SQL_CALC_FOUND_ROWS` 和 `GROUP BY expr DESC`,但 prepared under `ON` then execute under `OFF` 返回 rows 且 `warning_count=0`;flush plan cache 后仍复现,证明不是 physical plan cache。已入库 id30028。方法结论:S8 新增 prepared/preprocess semantic freeze:凡是 PREPARE-time validator 消费 session variable,都要用 direct current-session reference + prepared switch matrix + flush/off-cache 控制证明 EXECUTE 没有信任旧语义。
15. **S3 operator semantic arity refinement**(2026-07-03):遵守 id30027/id30028/id30029 暂停门,没有继续枚举 cache/prepared 变体,而是回到 `cluster_log` extractor 的源码 proof obligation。源码证明义务是 `message LIKE pattern [ESCAPE x]` 被替换成后端 regexp 后必须等价于 SQL scalar predicate;但 extractor 只保留 pattern,`CompileLike2Regexp` 固定 default backslash escape。testbed 上 custom ESCAPE fast arm 返回 0,CASE scalar reference 返回 130683,default-escape 控制 fast/reference 同为 130759。已入库 id30030。方法结论:S3 新增 "operator semantic arity" 预检查:替代 scalar operator 前先列出所有语义输入,包括 collation/timezone/precision/type domain/cache key/pattern syntax/escape char;只要 fast path 少保存一个输入且 drop scalar recheck,红格通常就是让这个输入改变真值的最小 SQL。
16. **S3 operator semantic arity cross-owner check**(2026-07-03):沿 id30030 的方法做第二 owner 复验,但没有枚举 cluster_log pattern。源码显示 `InfoSchemaBaseExtractor` 同样用 `extractLikePatternCol` 消费 `LIKE`,编译 regexp 后移除 scalar predicate。testbed 上 `information_schema.tables` custom ESCAPE fast arm 返回 `abc#x:self_true=0`,CASE scalar reference 返回 `abc_def:self_true=1`,default escape 控制为绿。已入库 id30031。方法结论:跨第二 owner 证明 blast-radius 后要立刻停,后续不扫所有 InfoSchema pattern-matchable 列;只在新的 omitted semantic input 或不同 replacement mechanism 上重开。
15. **S8 AST-mutation candidate**(2026-07-03):按 S8 reopen 条件找另一个 preprocessor/session switch,命中 candidate id30029。`hasAutoConvertWarning` 在非 strict `sql_mode` 下会把 overlong `VARCHAR` AST 改成 `TEXT/BLOB`;testbed 上 direct strict `CREATE TABLE ... VARCHAR(70000) CHARSET utf8mb4` 报 1074,但非 strict PREPARE 后切 strict EXECUTE 成功创建 `mediumtext`。因为 PREPARE 自身已 warning 1246,prepared DDL 是否应冻结 PREPARE-time normalization 需要 contract 裁决,所以入库为 candidate。方法结论:S8 分裂成 stale validation result(id30028 confirmed)和 stale AST mutation(id30029 candidate);后者更 contract-sensitive。S8 现在进入 guarded,不要继续普通 session-switch 枚举。
16. **S9 identity proof fast path**(2026-07-03):按用户纠偏回到 DDL 后,没有继续扩执行器/optimizer,而是从 `REORGANIZE PARTITION` 的 duplicate `_tidb_rowid` 修复路径里抽出 identity proof。源码注释已经说明 `EXCHANGE PARTITION` 后不同旧分区可能有 duplicate rowid;代码对 same target key + different raw bytes 会修 rowid,但对 same raw bytes 直接 `continue`。testbed 上构造两个旧分区都含 `(a,b,_tidb_rowid)=(1,1,1)` 后,`REORGANIZE PARTITION` 成功但 `COUNT(*)` 2->1。same rowid/different bytes 和 same bytes/different rowid 均为绿格。已入库 id600001。方法结论:S9 是 equality-as-identity fast path:必须显式列出 identity 需要的 owner/container/source 维度,然后用最小矩阵只改变遗漏维度。
17. **S10 DDL precheck metric mismatch**(2026-07-03):从 `MODIFY COLUMN` no-reorg-with-check 源码里抽出新证明义务:restricted SQL precheck 用 `LENGTH(col) > newFlen` 判断现有值是否适配目标类型。testbed 上 direct `varchar(3)`/`char(3)` 都接受 `_utf8mb4'中中中'`(`LENGTH=9,CHAR_LENGTH=3`),ASCII `abc` 缩列成功,但 `varchar(4)->varchar(3)` 和 `char(4)->char(3)` 对 `中中中` 均报 1265 且 schema 不变。已入库 id630001。方法结论:S10 是 validation metric mismatch:DDL 预检查的量尺必须与目标契约同单位,字节长度、字符长度、display width、encoded key bytes 不能混用。
18. **S10 target-state validation metric mismatch**(2026-07-03):没有扩 id630001 的 charset/string 变体,而是比较 FK 的 modify validator 与 create/add target-state validator。源码显示 FK 创建只要求 type/unsigned/charset/collation 兼容,测试里 parent `varchar(10)` / child `varchar(20)` 是通过用例;但 `isAcceptableForeignKeyColumnChange` 在 MODIFY 时额外要求 `newFlen >= originalFlen` 且 `newFlen >= relatedFlen`。testbed 上 direct target FK 结构 p10/c10、p10/c15、p15/c20 都能建;child `varchar(20)->varchar(10/15)` 报 1832,parent `varchar(10)->varchar(15)` with child `varchar(20)` 报 1833;child 20->25 和 parent 10->20 为绿格。已入库 id630002。方法结论:S10 泛化为 DDL validation metric mismatch:transition validator 不能比目标结构 validator 多套隐藏量尺,除非有明确 data-safety 理由。
19. **S10 partition-column transition allowlist mismatch**(2026-07-03):没有扩 FK type pair,而是换到 partition-column modify validator。源码显示 `checkPartitionColumnTypeChangeAllowlist` 在 target partition-definition validation 前只允许 string length extension。testbed 上 direct `varchar(5)` LIST/RANGE/KEY partition schema 都能建并插入 fitting rows;非分区 `varchar(6)->varchar(5)` 且 `MAX(CHAR_LENGTH)=3` 成功;partition `varchar(6)->varchar(5)` 在 LIST/RANGE/KEY 上均报 8200;partition `varchar(6)->varchar(7)` 为绿格。已入库 id630003。后续同一 selector 但换 D_dim 命中 id630023:direct `NOT NULL` partition schema 合法,非分区 `NULL -> NOT NULL` 在无 NULL 行时成功、有 NULL 行时由数据检查拒绝,但 RANGE/LIST/KEY/expr partition column 无 NULL 行仍报 8200。方法结论:coarse transition allowlist 不能替代 target-state/data-fit validator;如果最终 partition definitions 和 rows 都 fit,或已有通用数据检查能证明 row invariant,拒绝需要更精确的安全理由。
20. **S11 DDL dependency gate overbroad**(2026-07-03):没有扩 S10,而是打 generated-column dependency gate。源码显示 `checkModifyColumnWithGeneratedColumnsConstraint` 只证明 base column 被 generated column 引用,rename path 精确使用它,但 common modify path 后面无条件拒绝所有 MODIFY。testbed 上 direct `a int COMMENT 'new-comment', b as (a+1)` 成功且 `1->2`;direct `a int DEFAULT 5, b as (a+1)` 成功且默认插入 `5->6`;但 existing table 上 `MODIFY a int COMMENT ...` 和 `MODIFY a int DEFAULT 5` 都报 3106/3108。非依赖列 comment 和 generated column 自己 comment 为绿格,真正 base-column type change 仍拒绝。已入库 id630004。方法结论:dependency existence 不是 semantic-change proof;要把"依赖图存在"与"本次 DDL 是否改变依赖语义"拆开。
21. **S13 DDL shallow-copy target mutation**(2026-07-03):先用 S12 entrypoint-gap 重新撞到旧库 id1(CHECK×CHANGE COLUMN),识别为 duplicate 后立即停掉,转向新的 DDL reconstruction proof。源码显示 `BuildTableInfoWithLike` 对 `referTblInfo` 做浅拷贝,随后 `renameCheckConstraint` 为 target 重命名 CHECK 约束,但 `Constraints` 是 pointer slice,所以会污染 source `ConstraintInfo`。testbed 上 `src_auto` 初始 `src_auto_chk_1`,执行 `CREATE TABLE dst_auto LIKE src_auto` 后,`SHOW CREATE TABLE src_auto` 和非法插入错误都显示 `dst_auto_chk_1`;direct `d1/d2` 创建不会互相改名。已入库 id630005。方法结论:DDL target reconstruction 必须显式证明 nested metadata ownership;任何浅拷贝后原地 normalize target-only 字段的代码都要被小矩阵打 source/target isolation oracle。

---

## 6. TRUE-positive demo(已验证的"能抓真 bug",可复现)
**目的**:planted-bug 验证"挖出的 oracle 真能抓静默不一致 + 外部驱动管线通"。这正是之前缺的"故意有 bug 的靶子"。
- **靶子** `skipReorgWorkForTempIndex=return(true)`:源码 `pkg/ddl/index.go:1913` 强制 `skipReorg=true` → 跳过 `runReorgJobAndHandleErr`(ingest 路径的 temp-index 合并)→ 并发 DML 永不合并进索引。重现真实 bug 类 **A1.1 temp-index↔origin 合并漏做**(incomplete-fix 类)。
- **窗口** `beforeAddIndexScan=pause`:钉死在 write-reorg(temp index 已激活、backfill 未跑),比靠时序撞稳。
- **配方**:
  1. 建表灌 ~131072 行(doubling:`INSERT ... SELECT id+(SELECT COUNT(*) ...) ...` 重复 17 次),`UPDATE SET v=id` 让 v 唯一可差分。
  2. `curl PUT return(true) .../skipReorgWorkForTempIndex` + `curl PUT pause .../beforeAddIndexScan`。
  3. 后台 `ALTER TABLE t ADD INDEX vidx(v)`;轮询到 `ADMIN SHOW DDL JOBS 1` 的 schema_state = `write reorganization`。
  4. **用全新 id 区间**并发 `DELETE id BETWEEN a AND b` + `INSERT` 新 id 段(落 temp index)。
  5. `curl DELETE .../beforeAddIndexScan`(resume)→ 等完成 → `curl DELETE .../skipReorgWorkForTempIndex`。
  6. **oracle**:`ADMIN CHECK TABLE t`(主)+ 差分 `COUNT(*) USE INDEX(vidx)` vs `IGNORE INDEX(vidx)`。
- **结果**:`ALTER` rc=0(**静默成功**,naive 检查漏判);`ADMIN CHECK` → **ERROR 8223 data inconsistency ... index: vidx ... index-values:"" != record-values:[3000380]**;差分 via_index=130571 ≠ via_table=131071(漏并发插入的 500 行)。
- **三个必踩的坑**(都验证过):
  1. **必须 fast_reorg=ON(ingest)**:关 fast_reorg 走经典路径,并发 DML 直接双写真索引、无合并可跳 → 假阴性。
  2. **并发 DML 的 id 区间每轮要全新**:上轮 DML 改过的数据残留 → PK dup / 删 0 行 → 本轮无有效并发 → 假阴性。
  3. `ignoreReadIndexDupKey` 注 dup 不行:ingest 后有独立 dup 检测兜底,dup 仍被抓。
- **收尾**:drop demo 表;`SET GLOBAL tidb_ddl_reorg_worker_cnt=4, tidb_ddl_reorg_batch_size=256, tidb_ddl_enable_fast_reorg=ON`;失败点全 DELETE;干净表 add-index+ADMIN CHECK rc=0 确认无残留。

---

## 7. 下一步(主线 + 更硬的证明)
### 7a. 把 demo 固化成种子变异引擎(主线)
LINCHPIN 已通,直接落地。
- **种子** = `{schema, data, ddl, hold失败点, 变异后DML[], oracle}`。种子来源:bug 库 234 个回归测试(P0 add-index 带回归的:`SELECT id,fix_pr,LEFT(title,60) FROM v_bug WHERE ddl_family='add-index' AND has_regression_test=1 AND severity='critical';` 样例 46033/46508/47426/47954/47981/48136/48303/48724;回归测试在 `~/pc/tidb`,按 fix_pr `gh pr view <fix_pr> --json files` 找 `_test.go`)。
- **executor**(外部驱动,已验证可行):create+data → `curl PUT pause <hold失败点>` → bg `ALTER ADD INDEX` → 轮询到目标 schema 子状态 → 跑(变异后)并发 DML → `curl DELETE <hold失败点>` resume → `ADMIN CHECK TABLE` + 差分。
- **mutator** 变:列类型 / DML 算子(insert/update/delete/on-duplicate) / 失败点(inject_point)选择 / 并发度 / 分区·clustered / 拼接多 DDL。
- **种子配方模式**(范例 `pkg/ddl/tests/indexmerge/merge_test.go`):`create+insert` → `EnableCall(<reorg子状态点>, 并发DML回调)` → `add index` → `admin check`。外部翻译:`EnableCall` 回调 → 把该点 `pause` hold + 另一连接跑 DML + DELETE resume。关键 inject_point:`beforeAddIndexScan`(已验证 hold write-reorg)、`afterWaitSchemaSynced`(已验证 hold)、`afterBackfillStateRunningDone`、`beforeBackfillMerge`、`afterCheckTempIndexReorgCanSkip`、`create-index-stuck-before-public`。
- 注意 `mockDMLExecutionMerging` 是**回调型**(需 in-code Go 回调),外部设不了 → 外部用 `pause` + 外部并发 DML 近似。

### 7b. 非-planted 真命中(更硬的证明)
- 找 build 日期(**2026-06-22**)**之后才合入的真实 DDL bug 修复**:那些 bug 在当前 fp-tidb 上还活着 → 用种子引擎/复现去抓 = 非注入真命中。查 GitHub:component/ddl + type/bug + 修复 PR mergedAt > 2026-06-22。
- 或:coverage 引导的更长 fuzz;跨版本差分 oracle(再起一个稳定版 tidb 做 differential)。

### 7c. 继续挖 / 回填(并行可做)
- 剩余 bug 族:bootstrap/upgrade(20)、generic G1/G2(56/67)、other(83)、infoschema(18)、disttask(10)、query-planner(10)、test-infra(10)、infra/meta(10)。
- 回填 bug 库:各挖掘代理标出的 ~30 个 bug_type 误标(perf/OOM/infra/config/redaction/data-race/flaky)。

### 7d. 当前最新命中:partial index 蕴含判断 wrong-result(id30001)
**定位**:partial index 的 planner 可用性判断。根因锚点:
- `pkg/planner/core/operator/logicalop/logical_datasource.go:817` 调 `partidx.CheckConstraints` 决定是否保留 partial-index path。
- `pkg/planner/core/partidx/check_constraint.go:92-128` 用 ranger range + `UnionRanges` 证明 query filters 蕴含 partial condition;对上界型 partial 条件给了错误 true。

**最小稳定 hint 复现**:
```sql
DROP DATABASE IF EXISTS ai_native_pi_bug;
CREATE DATABASE ai_native_pi_bug;
USE ai_native_pi_bug;
CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, INDEX pi(b) WHERE a < 3);
INSERT INTO t VALUES (1,1,1),(2,2,2),(3,3,3),(4,10,4),(5,NULL,5);

SELECT id,a,b FROM t IGNORE INDEX(pi) WHERE a >= 0 ORDER BY b; -- 1,2,3,4
SELECT id,a,b FROM t USE INDEX(pi)    WHERE a >= 0 ORDER BY b; -- 1,2,漏 3,4
ADMIN CHECK TABLE t; -- 通过,说明索引内容没坏;错在 planner 认为 pi 可用
```

**无 hint 稳定复现(2026-06-30 新扩展)**:默认 session、fresh pseudo stats、未 `ANALYZE TABLE` 时,优化器会为了 `ORDER BY b LIMIT` 主动选择 `pi(b)` 并漏行。这不是 hint 人为强迫出来的 plan。
```sql
DROP DATABASE IF EXISTS ai_native_pi_nohint_min;
CREATE DATABASE ai_native_pi_nohint_min;
USE ai_native_pi_nohint_min;
CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(20), INDEX pi(b) WHERE a < 3);
INSERT INTO t VALUES
  (1,NULL,1,'null1'),(2,-1,2,'neg1'),(3,0,3,'zero'),(4,1,4,'one'),(5,2,5,'two'),
  (6,3,6,'three'),(7,4,7,'four'),(8,10,8,'ten'),(9,100,9,'hundred'),(10,NULL,10,'null2');

EXPLAIN FORMAT='brief'
SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t
WHERE a >= 0 ORDER BY b LIMIT 5;

SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t IGNORE INDEX(pi)
WHERE a >= 0 ORDER BY b LIMIT 5; -- 3,4,5,6,7

SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t
WHERE a >= 0 ORDER BY b LIMIT 5; -- 3,4,5,漏 6,7
```
`EXPLAIN` 证据:`IndexLookUp` + `IndexFullScan(Build) ... index:pi(b) keep order:true, stats:pseudo`。`ADMIN CHECK TABLE` 通过,说明仍是 planner 可用性错误,不是索引维护错误。

**已验证的同类形态**:
- `WHERE a <= 3` + 查询 `a >= 0` 也会错用 partial index。
- `WHERE a != 10` + 查询 `a BETWEEN 3 AND 10` 会漏掉 `a=10`。
- `WHERE a > 3`/`a >= 3` + 查询上界方向暂未复现,先记录为阴性证据。

**方法论结论**:这次命中验证了"分析 × fuzz"的新层次:先从 partial-index 源码推出蕴含不变量,再用小脚本枚举 `{partial 条件形状 × 查询谓词形状 × hint/无 hint × stats 状态}`,oracle 是 `IGNORE INDEX` vs `USE/FORCE INDEX` 行集差分。比纯随机 DDL/DML 更快触达 optimizer 正确性 bug。

### 7e. id30001 命中后的暂停门复盘(当前应先完成)
**暂停原因**:用户明确要求"发现新 bug 后,应该先停下来总结和提炼方法论,再继续"。因此不要直接继续 fuzz/矩阵跑量。

已完成:
- 最小复现:见 §7d SQL。
- oracle 命名:planner 可用性 oracle / index-table 行集差分 oracle。
- 初步根因模型:partial-index 条件必须被查询谓词蕴含;`partidx.CheckConstraints` 的 range 证明对部分条件形状不安全。
- 资产回填:`found_bug id30001` 已入库;方法论文档已新增 `9.1 命中后暂停门`。
- issue/repro 草案:`/Users/bba/pc/ai-native-partial-index-id30001-draft.md`。
- 工具状态:`/Users/bba/pc/ai_native_partial_index_probe.py` 已新增 `--matrix` 模式和语义族标签,并通过 `py_compile`。
- 复盘后矩阵已跑:`/Users/bba/pc/ai_native_partial_index_semantic_matrix_20260630_184141.csv`;1200 格子、280 mismatch,无独立新根因,但扩大了 id30001 blast radius。
- no-hint/stats-pressure 小矩阵已命中同根因更强形态:默认 no-hint 查询因 `ORDER BY b LIMIT` 使用 `pi(b)` 漏行。按暂停门停止,未继续扩大矩阵。

已完成:
1. 写清楚"为什么 work":AI 提高的是目标密度、证明义务精度、oracle 灵敏度,不是执行吞吐。
2. 把 id30001 归类为 **proof-obligation fuzzing**:先找代码里的证明器/判定器,再生成反例族攻击它。
3. 补充边界表:上界 partial 条件 + 向右扩张查询会错;excluded point + 连续区间会错;lower-bound/point/NULL/多数 IN 和 OR 是阴性。
4. 将 `--matrix` 从"条件字符串枚举"推进到"语义反例枚举":区间、excluded point、NULL、OR widening 等均有 family 标签。
5. 复盘后的矩阵已跑完;这轮应作为方法论资产,不是新 bug 计数。
6. 新增 no-hint 方法论修正:`ORDER BY b,id` 虽然让结果更确定,但会让单列 `pi(b)` 不再满足完整排序,从而避开目标 fast path。正确做法是让 `b` 本身唯一,再用 `ORDER BY b LIMIT` 保持确定性和 fast-path 压力。

### 7f. 下一轮搜索协议:proof-obligation fuzzing
目标不是"写更多 SQL",而是让 AI 系统地找出代码里哪些判定器在替系统做语义证明,再攻击这些证明。

**AI 步骤**:
1. 建证明器候选目录:优先 grep/阅读 `Check*`, `CanUse*`, `Need*`, `Prune*`, `Derive*`, `Imply*`, `Rewrite*`, `fast path` guard,以及返回 bool 后影响 plan/path/skip/block 的函数。
2. 对每个候选抽三元组:
   ```text
   前提 P:代码实际检查了什么
   结论 Q:返回 true 后系统相信什么
   后果 F:相信 Q 后会跳过什么安全路径或选择什么 fast path
   ```
3. 让 AI 生成反例族,不是随机 case:
   ```text
   P 看似成立但 Q 不成立
   Q 对 NULL/边界/OR/类型转换/collation/统计状态/参数化不封闭
   fast path 与 safe path 在同一稳定用户表上可差分
   ```
4. 给每个反例族配 oracle:
   - fast path vs safe path 行集一致。
   - 同义 SQL / hint on-off / session var on-off / stats state on-off 结果一致。
   - 用户表稳定、查询确定、错误码和 warning 也纳入差分。
5. 只在小样本命中后再规模化,并保存阴性格子,避免把矩阵做成盲枚举。

**partial-index 下一轮最小协议**:
```text
proof target: partidx.CheckConstraints
P/Q: query filters imply partial-index condition
counterexample families:
  partial overlap ranges
  excluded point: a != c vs query range contains c
  NULL leakage: IS NOT NULL / <=> NULL / OR IS NULL
  OR widening: safe branch OR unsafe branch
  collation/type boundary for string/date predicates
fast-path toggles:
  USE/FORCE INDEX(pi)
  IGNORE INDEX(pi)
  no hint under ORDER BY/stats pressure
  prepared plan cache with parameter change
oracle:
  row-set equality + plan evidence + admin check only as storage sanity
```

**partial-index semantic matrix 当前实验状态**:
- `/Users/bba/pc/ai_native_partial_index_probe.py` 已支持 matrix family 标签。
- 扩展运行:`/Users/bba/pc/ai_native_partial_index_semantic_matrix_20260630_184141.csv`。
- 规模:15 个 partial 条件语义 × 20 个查询谓词语义 × 4 个 order/limit × `{USE, FORCE, no_hint}` = 1200 格子。
- 结果:280 mismatch,分布为 `upper_bound/lower_overlap=138`, `upper_bound/wide_range=72`, `upper_bound/boundary_range=52`, `excluded_point/boundary_range=12`, `upper_bound/or_widening=6`。
- 阴性:lower-bound partial 条件、point、NULL、多数 `IN`/OR 点集没有命中。
- `no_hint` 复盘:语义矩阵里没稳定 mismatch 的原因不是无 hint 安全,而是当时使用 `ORDER BY b,id`,单列 `pi(b)` 不能完整满足排序,导致目标 ordered-index fast path 没被触发。改成唯一 `b` 数据 + `ORDER BY b LIMIT 5` 后,默认无 hint 已复现 wrong-result。
- 方法反馈:不要把每个 hit 当新 bug;这是 id30001 同根因 blast radius。partial-index 当前已回答"hint 下会错"和"默认可见";下一步应转 plan-cache/partition 等新证明义务,或进入 issue/fix 验证。

**partition-pruning 当前实验状态**:
- 已新增 `/Users/bba/pc/ai_native_partition_prune_probe.py`,并通过 `python3 -m py_compile`。
- 已在 `fp-tidb` 上跑一轮三方差分:未分区参考表 vs `tidb_partition_prune_mode='static'` vs `'dynamic'`。
- 覆盖初始边界表:`RANGE COLUMNS(int)`、`RANGE COLUMNS(a,b)`、`RANGE COLUMNS(date)`;结果 `findings=0`。
- 方法改进点:不要只比较 static/dynamic,因为两条 pruning 路径可能同错。之后 partition 类 oracle 必须保留未分区参考表。
- 后续扩展已完成一版:探针现在从边界自动派生谓词,并新增 unsigned 多列、字符串 range columns、`floor(unix_timestamp(ts))` 表达式分区;完整扩展跑结果仍是 `findings=0`。
- 工具改进:新增 `--max-predicates` 和 `--progress-every`,避免下一轮扩大时变成黑盒。
- 下一步建议转向 plan-cache parameter drift:验证"某个证明义务在第一次参数下成立,缓存后换参数是否仍安全"。partition 继续保留,但别只对同六张 schema 增加更多同质谓词。

**plan-cache parameter drift 当前实验状态**:
- 已新增 `/Users/bba/pc/ai_native_plan_cache_drift_probe.py`,并通过 `python3 -m py_compile`。
- oracle 为三方差分:prepared cached execution vs prepared cache-disabled execution vs direct literal execution。
- 已修正一次探针观测 bug:`EXECUTE` 后不能先跑独立 marker query,否则会覆盖 `@@last_plan_from_cache`;现在用 `SELECT 'LAST', key, @@last_plan_from_cache` 立即取证。
- baseline 已确认能观测 cache hit:`point_get_cache_baseline`、`normal_index_range_cache_baseline` 后续参数 `last_plan_from_cache=1`,且结果与 direct/nocache 一致。
- 初始 proof cases 未命中:`partial_index_gt_threshold`、`partition_range_boundary` 可观察到部分 cache hit 但 `findings=0`;`partial_index_is_not_null_nulleq` 和 `predicate_simplification_null_in` 当前未进入 cache。
- 方法反馈:plan-cache drift fuzzing 的第一步不是扩大参数,而是筛出"会进入 cache 的证明器";未进入 cache 的 case 只能证明 TiDB 的保守门有效,不能证明 drift oracle 覆盖到目标。
- 代码阅读补充:`DataSource.CheckPartialIndexes` 对 plan cache 只允许 single `IS NOT NULL` partial-index 通过 `AlwaysMeetConstraints`;普通比较型 partial index 会被标 `IndexScan of partial index is uncacheable`。因此 plan-cache drift 的 partial-index 分支应聚焦 NULL-reject proof,不要继续拿 `a < c`/`a > c` comparison partial index 做缓存漂移目标。
- 2026-06-30 follow-up:探针已支持多参数 schedule,新增 `LIMIT ?` 负对照、动态分区 between、LIST/default partition × plan-cache。`LIMIT ?` 证实会进入 cache key;LIST/default 能观察到 cache hit 但三方结果一致,`findings=0`。

### 7g. 当前最新命中:id30002 candidate predicate-simplification/collation wrong-result
**定位**:谓词化简/表达式化简在合并 `IN` 和 `!=` 时没有保持 collation/coercibility 语义。

最小复现:
```sql
DROP DATABASE IF EXISTS ai_native_pred_min;
CREATE DATABASE ai_native_pred_min;
USE ai_native_pred_min;

CREATE TABLE t(
  id INT PRIMARY KEY,
  s VARCHAR(8) COLLATE utf8mb4_general_ci
);
INSERT INTO t VALUES (1,'a'),(2,'A'),(3,'b');

SELECT id,s,
       s IN ('a','A') AS in_pred,
       s != _utf8mb4'A' COLLATE utf8mb4_bin AS ne_pred,
       (s IN ('a','A')) AND (s != _utf8mb4'A' COLLATE utf8mb4_bin) AS both_pred
FROM t ORDER BY id;

SELECT id,s
FROM t
WHERE s IN ('a','A')
  AND s != _utf8mb4'A' COLLATE utf8mb4_bin
ORDER BY id; -- 错误返回 1:a,2:A

SELECT id,s
FROM t
WHERE CASE
        WHEN (s IN ('a','A') AND s != _utf8mb4'A' COLLATE utf8mb4_bin)
        THEN 1 ELSE 0
      END = 1
ORDER BY id; -- 正确返回 1:a
```

证据:
- 投影求值显示 `A` 上 `ne_pred=0`,组合谓词为 false。
- 普通 WHERE 返回 `a` 和 `A`,CASE-wrapped oracle 只返回 `a`。
- 有无二级索引都复现;无索引表的 `EXPLAIN FORMAT='brief'` 里 pushed selection 已经只剩 `in(t.s, "a")`。
- 控制组 `s IN ('a') AND s != _utf8mb4'A' COLLATE utf8mb4_bin` 正确只返回 `a`,说明问题发生在 `IN('a','A')` 与 `!= binary 'A'` 的合并阶段。

源码锚点:
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:151` `updateInPredicate`
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:168` 用 `value.Equal(evalCtx, notEQValue)` 判断 IN list 中哪个值可删
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:250` `mergeInAndNotEQLists` 在更新 IN 后删除 `!=` 谓词
- sibling equality contradiction 分支有 string collation compatibility guard;IN/!= merge 分支目前缺类似保护。

资产:
- 复现/方法论草案:`/Users/bba/pc/ai-native-predicate-simplification-collation-draft.md`
- 探针:`/Users/bba/pc/ai_native_predicate_simplification_probe.py`
- 第一轮探针结果:800 predicates,2 mismatch,二者是同根因的正反谓词顺序;600+ integer/NULL scalar/OR 和 `IN`/`!=` 小样本为阴性。

方法论结论:
- predicate simplification 缺少显式 safe-path toggle 时,`WHERE P` vs `WHERE CASE WHEN P THEN 1 ELSE 0 END = 1` 是有效低噪 oracle。
- 这次命中进一步证明"AI 找证明器 + 写语义 oracle"比盲 fuzz 更高效:计划/分区/缓存阴性后,换到"会删除谓词"的 proof target,用 800 个小样本即命中 wrong-result。
- 命中后不要继续扩大矩阵;先完成暂停门,再决定是否专门扩展 collation/coercibility 族。

### 7h. 2026-07-10 severe DDL real-cluster probe 校准
**目标**:把历史 severe DDL seed 从“源码/issue 认知”升级成“testbed 上可重复跑的相位注入探针”,重点验证 `phase oracle + topology fault + strong oracle` 这条 loop 能否稳定工作。

**新增探针**:
- `/Users/bba/pc/ai-native-probes/multischema_change_row_oracle_probe.go`
  - 形状:并发 DML + 往返 `CHANGE COLUMN x<->a, y<->b`
  - oracle:`admin check table` + 当前 ordinal position 列名下的行值公式校验
- `/Users/bba/pc/ai-native-probes/add_index_pd_bounce_oracle_probe.go`
  - 形状:大表 + 多 region `ADD INDEX` / multi-index add-index
  - 注入:DDL running 期间 `kubectl delete pod tc-pd-0 --force --grace-period=0 --wait=false`
  - oracle:`admin check table` + `admin check index` + 行数守恒

**testbed 校准结果(集群 8220955, classic + ingest + DXF)**:
- `multischema_change_row_oracle_probe.go -run-for 40s`
  - 40s 内多轮 DDL 往返与并发 DML,`admin check` 与公式 oracle 全绿。
  - 结论:issue62531 / issue63258 邻域在当前 master 上的基础 user-level 形状已被补强,这条线不能再靠“重复旧 workload”吃红。
- `add_index_pd_bounce_oracle_probe.go -rows 600000 -regions 256 -pd-bounces 2 -bounce-interval 4s -ddl-shape single`
  - 单索引 add-index 在 running 期间命中两次 PD bounce,DDL 最终 `synced`,`admin check table/index` 与行数守恒全绿。
  - 结论:issue59701 的直接邻域在当前 master 上通过了 real-cluster topology fault 校准。
- `add_index_pd_bounce_oracle_probe.go -rows 600000 -regions 256 -pd-bounces 2 -bounce-interval 4s -ddl-shape multi`
  - multi-schema add-index (`add unique index uk_c(c), add index idx_d(d)`) 在 running 期间同样命中两次 PD bounce,最终 `admin check table/index` 全绿。
  - 结论:历史 severe seed 的交叉组合(`59701 topology fault` × `61255 multi-index/multi-schema`) 当前也未命中新红格。

**这轮最重要的方法论收获**:
- 不能只复用“issue 名字”;要复用它背后的**证明义务**。`59701` 的核心不是“kill PD leader”,而是“backfill region ranges 必须连续完整覆盖,不能把部分覆盖误当完整覆盖”。
- 不能只复用“job_type 字面值”;multi-index add-index 在 DDL 视角里是 `alter table multi-schema change`,观察器如果只筛 `add index` 会直接错过 running 窗口。
- 真实集群探针也需要自己的 pause gate。先做小 smoke 校准探针本身,否则容易把 probe 自身观测 bug 误当系统红点。
- 这条 loop 现在已经具备四个可复用部件:
  1. **proof seed**:历史 severe issue / fix diff / 代码假设
  2. **phase oracle**:DDL running window 的精确识别
  3. **fault schedule**:拓扑扰动或 owner/topology 注入
  4. **strong oracle**:`admin check` / row formula / row-count conservation

**下一步建议**:
- 继续沿 severe DDL 走,但要把 fault 从“单一 PD bounce”扩成更贴近真实故障编排的 schedule:
  - PD bounce + 并发 DML
  - TiDB owner bounce + add-index / multi-schema change
  - checkpoint / resume / owner handoff 相关的 phase-aware fault
- 这比回头再扫一遍 partition 或重复 moderate 级 case 更符合当前目标:验证并增强 AI-native severe bug discovery loop。

### 7i. 2026-07-10 owner-fault probe 与 handoff 校准
**新增探针**:
- `/Users/bba/pc/ai-native-probes/add_index_owner_fault_oracle_probe.go`
  - 支持 `dist_task={on,off}`、`ddl-shape={single,multi}`。
  - 支持 `fault-mode={delete-pod,delete-owner-pod,resign-owner}`。
  - `delete-owner-pod` 不是写死 pod 名,而是每次先 `ADMIN SHOW DDL` 读取 `OWNER_ADDRESS`,再解析出当前 owner pod 并删除。
  - terminal oracle 不只看 job state,还强制检查:
    - job state vs 可见 schema 是否一致
    - 索引是否真的可用(`USE INDEX` count)
    - `ADMIN CHECK TABLE` / `ADMIN CHECK INDEX`
    - 行数守恒

**这轮实际跑过的 owner-fault 负校准**:
- 单 TiDB owner restart(同 pod PVC 重启)：
  - `dist_task=on`, single add-index, 100k/600k 行, delete owner pod 后 `synced` + `admin check` 全绿。
  - `dist_task=off`, single add-index, 600k/150w 行, delete owner pod 后 `synced` + `admin check` 全绿。
- 双 TiDB 真 owner handoff(临时把 tc.tidb.replicas 从 1 拉到 2,跑完收回 1)：
  - `dist_task=off`, single add-index, 150w 行, `fault-mode=delete-owner-pod`, running 后 4s 删除当前 owner pod,最终 `synced` + schema/index/oracle 全绿。
  - `dist_task=off`, multi-schema add-index(`unique c` + `idx d`),150w 行,两次删除当前 owner pod,最终 `synced` + schema/index/oracle 全绿。

**重要方法论收获**:
- `OWNER_TOPOLOGY_HANDOFF` 不能只打“固定 pod crash”。单 pod restart 更多是在测“同一 owner 重启”;真正高价值的是“当前 owner 切到另一台 TiDB 之后,本地状态/引擎/checkpoint 是否还能自洽”。
- `ADMIN SHOW DDL` 是 owner-fault loop 的关键 observer。它给出 `OWNER_ADDRESS`,让 fault scheduler 从“猜谁是 owner”升级为“删当前 owner”。
- terminal oracle 必须从“job state”升级到“job state + schema visibility + index usability + admin check”。这正是为了吃 `state says cancelled/rollback but index actually exists` 这类强后果。
- 这轮还暴露了 probe 自身两条需要补强的观察纪律:
  1. `information_schema.ddl_jobs.state` 在当前版本里可能返回 `done`,不能只枚举 `synced`。
  2. 多次 fault schedule 之间,必须在每一击之前重新确认 job 仍处于 running/active phase,否则后续 fault 可能只打在 DDL 已结束的尾巴上。

**当前判断**:
- 截至这轮,`add-index owner handoff` 这条 severe 邻域在 current master 上没有打出新的 C3 红点。
- 但 owner-fault probe 已经变成可复用资产,后续可以直接拿去接:
  - 更强的 phase gate(例如等 row_count/progress/特定 schema_state)
  - add-index + 并发 DML
  - owner handoff + rollback / cancel / checkpoint-resume
  - 已知高危 proof seed 的交叉组合

### 7j. 2026-07-10 full-reorg `MODIFY/CHANGE COLUMN` severe harness 校准
**新增/补强资产**:
- `/Users/bba/pc/ai-native-probes/go.mod`
  - 让 `ai-native-probes` 目录里的 Go probe 可以直接复跑,不再依赖临时环境。
- `/Users/bba/pc/ai-native-probes/modify_column_owner_fault_oracle_probe.go`
  - 从最初的 `change x->a, y->b smallint` 形状,重定向到源码证明为 **必进 full reorg** 的 shape:
    - `ALTER TABLE ... CHANGE COLUMN x a VARCHAR(16) NOT NULL`
    - 表上保留 `KEY idx_x(x)` / DDL 后 `idx_x(a)`,让 row rewrite 和 index 面都进入强 oracle。
  - 并发 workload 改成真正的 delete/reinsert:
    - 每个 worker 只打自己 shard,避免自冲突噪音。
    - 先 `DELETE` 再 `INSERT` 同一批主键,保留公式值 `a=CAST(id%10000)` / `y=id%10000*2`。
  - terminal oracle 升级为:
    - 行数守恒
    - 全表公式一致性
    - probe hot rows point read
    - `USE INDEX(idx_x)` vs `IGNORE INDEX(idx_x)` 计数一致
    - `ADMIN CHECK TABLE`
  - observer 纪律补强:
    - final oracle 改用 fresh session,避免旧 session schema cache 反复撞 transient error。
    - 增加 `job_id` watermark,避免同一条 DDL 文本重复运行时被历史 `ddl_jobs` 污染。

**这轮先踩到的 probe 级坑**:
- `INT -> SMALLINT` 这类 shape 在当前源码下是 `NoReorgWithCheck`,不是 severe seed 要的 changing-column/full-reorg 路径。
  - 直接证据:`pkg/ddl/modify_column.go:getModifyColumnType` + `needRowReorg`。
  - 方法论结论:不能只看“MODIFY/CHANGE COLUMN”字面值;必须先从源码证明它真会进目标 phase。
- DML harness 里把 `duplicate entry` 一律当成 retry 会把“insert 已成功但回包丢了”的场景误判成无限重试。
  - 修正:对 insert 路径,duplicate 视为 success;同时 stop 采用 graceful drain + cancel fallback。
- final oracle 最初卡在静默重试,不是产品挂住,而是旧 session 上的 transient schema error 被 `queryInt` 吞掉。
  - 修正:只重试已知 transient,并打印重试原因;final oracle 改 fresh session。
- 索引 oracle 最初错把“命中 1 行”当成契约,但本 workload 是 `%10000` 重复值。
  - 修正:改成 index path vs table path 计数一致。

**实际跑过的 full-reorg 负校准**:
- baseline:
  - `100k rows + 4 workers + delete/reinsert during write reorganization`
  - 自动跑通并 clean exit,最终 `final oracle green, cols=[a y] probe_ids=[1 2001 4001 6001]`。
- owner handoff:
  - `100k rows + 4 workers + fault-mode=delete-owner-pod + dual TiDB`
  - 观测到真实连接 reset、owner `tc-tidb-1 -> tc-tidb-0` handoff,最终 `synced` + final oracle 全绿。
  - 再次复跑(修完 `job_id` watermark 后)也观测到真实 handoff `tc-tidb-0 -> tc-tidb-1`,最终仍全绿。
- 更强一格:
  - `200k rows + 8 workers + delete-owner-pod`
  - 第一次 probe 因 observer 误读历史 job 提前退出,但集群上的真实 job `591` 继续跑完;外部 oracle 复核:
    - `COUNT(*) = 200000`
    - `bad = 0`
    - `USE INDEX(idx_x)` 与 `IGNORE INDEX(idx_x)` 计数一致
    - `ADMIN CHECK TABLE` 返回成功
  - 这说明“full reorg modify-column + concurrent delete/reinsert + owner handoff”在 current master 上依然没有直接打出 C3 红点。

**这一轮最重要的方法论收获**:
- 对 severe DDL,`proof target` 的第一步不是“写 workload”,而是先从源码把目标压成:
  - 这条 SQL 会不会真的走 full reorg?
  - 观测窗口是不是目标 phase?
  - 复跑时会不会被历史 job 污染?
- 这也进一步证明了一个更具体的 loop:
  1. 先从 `getModifyColumnType` / `needRowReorg` 之类源码 owner 找到 **phase 证明义务**
  2. 再把 workload 压成小矩阵
  3. 最后用强 oracle + phase observer 去打红格
- 当前 `issue62531` severe lane 在 master 上的直接邻域已经从“旧 workload 绿”推进到“full reorg + concurrent DML + owner handoff 绿”。如果还要继续逼近 C3 红点,下一步不该再盲加规模,而是:
  - 在 TiDB 内部加 failpoint / hook,把 delete/reinsert pin 到更窄的 row-rewrite 子窗口
  - 或专门去找 sibling path / rollback-resume / changing-column mid-flight 恢复这类更尖的 phase split

**当前判断**:
- 截至这轮,`DDL_ROW_IMAGE_RECONSTRUCTION` 这条 severe lane 还没有在 current master 上打出新的 confirmed C3。
- 但我们已经把“源码证明 full reorg → phase observer → owner fault / concurrent DML → 强 oracle”这套方法打磨成了可复用资产,而且明确知道下一步该往 failpoint-pinned 子窗口推进,而不是回到宽泛 blind fuzz。

### 7k. 2026-07-10 owner handoff + cancel 路径打出新 wrong-error 信号
**在 7j 的 probe 基础上继续加的控制面**:
- `modify_column_owner_fault_oracle_probe.go` 新增:
  - `control-mode={none,pause-resume,cancel}`
  - `control-min-row-count`
  - `control-pause`
- 先验证了 `pause-resume`:
  - `100k rows + full reorg + concurrent delete/reinsert + delete-owner-pod + pause-resume`
  - 观测到 `paused -> queueing -> running -> synced` 的完整状态链,最终 final oracle 全绿。
- 再验证 `cancel`:
  - `200k rows + full reorg + concurrent delete/reinsert + control-mode=cancel`
  - **无 owner fault** 时,用户层正常报错:
    - `Error 8214 (HY000): Cancelled DDL job`
    - `ddl_jobs.state=rollback done`
    - 最终 schema 回到 `[x y]`,数据/`ADMIN CHECK TABLE` 全绿。
  - **叠 owner handoff** 时,用户层报错变成:
    - `Error 8235 (HY000): DDL job rollback, error msg: DDL reorg element does not exist`
    - 同时 `ddl_jobs.state=rollback done`
    - 最终 schema 仍回到 `[x y]`,数据/`ADMIN CHECK TABLE` 全绿。

**这个信号的意义**:
- 这不是 C3 data corruption,因为 rollback 结束后的 schema/data/index strong oracle 全部是绿的。
- 但它是一个新的 **stress-specific wrong-error**:
  - 同样的 cancel,
  - 不打 owner handoff 时是正常 `Cancelled DDL job`,
  - 一旦叠 owner handoff,用户就收到内部味很重的 `DDL reorg element does not exist`。
- 这说明 `owner handoff + cancelling` 确实打到了恢复路径的边缘条件,而且不是 probe 自己伪造出来的:
  - oracle 证明最终回滚是干净的,
  - 差异只存在于 user-visible error surface。

**源码锚点(待继续深挖)**:
- `pkg/ddl/reorg.go` 对 `job.IsCancelling() && ErrDDLReorgElementNotExist` 有一条“不要继续当硬错误”的兜底。
- 但实测 owner handoff + cancel 仍把 `ErrDDLReorgElementNotExist` 冒泡到用户侧,说明真实执行路径大概率绕开了这条 special-case,或者在更早的 owner/reorg-handle 恢复路径上就把错误固化进了 rollback surface。
- `pkg/ddl/rollingback.go` 里,一旦 canceling 过程中碰到“非 `ErrCancelledDDLJob`”的错误,会把它包装成 `DDL job rollback, error msg: ...` 返回给用户;这和实测错误形状一致。

**方法论收获**:
- `pause-resume` / `cancel` 这类控制面动作必须有自己的终态纪律:
  - `cancelling` 不是终态,不能拿来做最终 oracle。
  - 只有 `rollback done` / `cancelled` 才能证明 rollback surface 稳定。
- 这轮也证明了另一个重要点:
  - 即使没有直接打出 C3,**“同 oracle 绿、唯独 user-visible error 变脏”** 仍是高价值 signal。
  - 它说明 loop 已经进入了恢复路径的脆弱区,后面再加 failpoint-pinned 子窗口时,命中更强后果的概率会明显高于 blind fuzz。

**当前判断**:
- `owner handoff + cancel` 现在至少已经是一个可复现的 wrong-error 候选,但还不是我们要的 severe C3。
- 下一步若继续沿这条线逼近 severe,优先级应是:
  1. 在 row-rewrite 更窄的子窗口里 pin cancel / owner handoff 的先后顺序
  2. 看能否把“wrong-error only”升级成“schema/data/index inconsistency”或“rollback surface stuck / dependent DDL blocked”
  3. 否则把它作为恢复路径邻域的 moderate hit 记账,然后把 severe 搜索继续推进到 failpoint-pinned 子窗口
    或另一个 sibling path。

### 7l. 2026-07-10 add-index active-window PD bounce 打出新的 availability RED
**这轮先修的不是 workload,而是 observer**:
- 之前 `add_index_pd_bounce_oracle_probe.go` 用 `ddl_jobs.row_count` 当 active-window gate,结果在 current master 上观测到:
  - DDL session 已经返回 success,
  - `information_schema.ddl_jobs` 仍短暂显示 `state=running`,
  - `row_count` 甚至会在 DDL 返回之后才跳到最终值。
- 这意味着旧 probe 会把 PD bounce 打在“DDL 已结束但 metadata 还没完全收口”的尾迹上,绿结果没有证明力。
- 因此 probe 新增三个 gate:
  1. **DDL session live**:只有 DDL goroutine 还没返回,才允许 fault schedule 进入；
  2. **schema_state gate**:`fault-schema-state=write reorganization`；
  3. **active duration gate**:`fault-min-running=3s`,确保 fault 不是刚进 running 就被误打。

**probe 资产补强**:
- `/Users/bba/pc/ai-native-probes/add_index_pd_bounce_oracle_probe.go`
  - 新增 `-fault-min-running`
  - 新增 `-fault-schema-state`
  - 修正:即使 `ddl_jobs` 还显示 `running`,只要 DDL session 已返回,就不再把它当 active window
  - 新增 `-dist-task on|off`,方便命中后立即反推 selector

**当前 testbed 上打出的最小矩阵(8220955, 150w rows, 256 regions, active window=write reorganization for >=3s)**:
- `multi-schema add-index + 1 PD bounce`
  - SQL:`alter table ... add unique index uk_c(c), add index idx_d(d)`
  - 结果:GREEN
  - DDL 最终 `synced`; `ADMIN CHECK TABLE/INDEX` 通过; 行数守恒。
- `multi-schema add-index + 2 PD bounces`
  - 同一 active window,第 2 次 bounce 后用户层报错:
    - `Error 1105 (HY000): create TSO stream failed, retry timeout`
  - `admin show ddl jobs`:
    - parent `alter table multi-schema change`: `rollback done`
    - subjob `add index /* subjob */`: `rollback done`, `row_count=1500000`
  - rollback 后 external oracle:
    - `SHOW CREATE TABLE` 只有 `PRIMARY KEY`
    - `ADMIN CHECK TABLE` 通过
    - 行数守恒
  - 结论:不是数据损坏,而是 **用户 DDL 在短时重复 PD fault 下被错误打穿为 rollback failure surface**。
- `single add-index + 2 PD bounces`
  - SQL:`alter table ... add index idx_c(c)`
  - 结果:同样 RED,同样用户层 `create TSO stream failed, retry timeout`
  - `admin show ddl jobs`: `add index ... rollback done`
  - rollback 后 external oracle 同样全绿。
- `single add-index + 2 PD bounces + dist_task=off`
  - 结果:仍然 RED,同样 `create TSO stream failed, retry timeout`
  - `admin show ddl jobs`: `rollback done`
  - rollback 后 schema 仍只剩主键,`ADMIN CHECK TABLE` 通过。
- `single add-index + 2 PD bounces + dist_task=off + fast_reorg=off`
  - 结果:GREEN
  - DDL 最终 `synced`; `ADMIN CHECK TABLE/INDEX` 通过; 行数守恒。
  - `last ddl job` 明确变成 `txn, thread=1, batch_size=32, max_node_count=3`,不再是 `ingest...` 路径。
- `single add-index + 2 PD bounces + dist_task=off + fast_reorg=on + 100w rows`
  - 结果:RED
  - 同样用户层 `create TSO stream failed, retry timeout`
  - `admin show ddl jobs`: `rollback done`
  - rollback 后 schema 仍只剩主键,`ADMIN CHECK TABLE` 通过,`COUNT(*)=1000000`
- `single add-index + 2 PD bounces + dist_task=off + fast_reorg=on + 60w rows`
  - 结果:GREEN,但原因不是 lane 消失,而是 DDL 约 3s 内结束,第 2 次 bounce 之前 active window 已收口。
  - 方法论上这格是 schedule-too-short,不是反例。

**这次 RED 的质量判断**:
- 这不是 `issue62531`/`61255` 那类 data corruption severe。
- 但它已经不是 moderate wrong-error 级别的“只脏错误文案”了,而是更靠近 severe 目标中的 **DDL availability / liveness break**:
  - 用户发起的 add-index 进入真实 write-reorg active window,
  - PD 发生两次短时重启,
  - 集群恢复 ready 之后,DDL 仍以 `create TSO stream failed, retry timeout` 失败并回滚。
- 从用户视角看,这意味着 **短时 topology fault 可以把一个本应可恢复的 add-index 直接打成失败**,而不是平滑重试后继续完成。

**为什么这次方法会 work**:
- 不是因为“多打了几次 PD”,而是因为 observer 终于证明了 fault 真落在了 **consequence-relevant window**。
- 同一个历史 seed(`59701 topology fault`)在旧 observer 下是 GREEN,换成“DDL live + write reorg + active-for>=3s”后立刻打出稳定 RED。
- 这说明 severe DDL 环境故障 lane 的关键瓶颈不是 fault 注入本身,而是:
  - 能不能证明 fault 打在正确语义窗口；
  - 能不能把 matrix 压到只剩 1~2 个 schedule 维度,让红/绿边界一眼可见。

**当前反推出的 selector 候选**:
- 现在可以把这个 lane 进一步收窄成 `FAST_REORG_TSO_RECOVERY`:
  - P:fast-reorg/ingest add-index 在 active reorg 中依赖持续可恢复的 TSO stream / retry path；
  - Q:短时 PD failover 后,这条恢复路径仍足以继续完成 reorg；
  - F:重复短时 PD bounce 会把 fast-reorg add-index 打成 `create TSO stream failed, retry timeout`,最终 rollback；而切回 `txn` 路径后同样 schedule 为 GREEN。

**当前源码锚点候选**:
- `pkg/ddl/ingest/checkpoint.go:422-430`
  - `CheckpointManager.afterImport()` 直接 `pdCli.GetTS(s.ctx)`；
  - 一旦报错,仅记录 `advance watermark get ts failed` 后原样返回,没有像 leader-change 那样的恢复性重试。
- `pkg/ddl/ingest/checkpoint.go:542-549`
  - `resumeOrInitCheckpoint()` 在 checkpoint 没有 TS 时也直接 `pdCli.GetTS(s.ctx)`；
  - 同样是一次性分配 TS,失败即返回。
- `pkg/ddl/ingest/backend_mgr.go:140-163`
  - fast-reorg/ingest 路径会创建 `CheckpointManager`；
  - 这与 live matrix 中 `fast_reorg=on` 红、`fast_reorg=off` 绿吻合。
- 对照组:`pkg/ingestor/ingestctrl/checksum.go:382-398`
  - checksum 路径遇到 PD leader-change/TSO stream 错误时,会显式循环重试；
  - 这说明仓内已经存在“PD 短时 failover 不应被直接当硬失败”的先例。

**目前最像的根因解释**:
- fast-reorg add-index 在 import / checkpoint watermark 推进阶段需要向 PD 再拿一次 TS；
- repeated PD bounce 让 `pdCli.GetTS` 返回 `ErrClientCreateTSOStream(... retry timeout)`；
- 这条路径当前没有 leader-change / stream-closed 级别的恢复性重试,导致错误直接冒回 DDL reorg 主路径,最终把 job 打成 rollback。

**下一步建议**:
1. 先不要继续盲加更多 schedule 维度；这格已经足够证明 observer 改进带来了真实新信号。
2. 下一步优先做源码锚点确认:
   - 读 `runReorgJobAndHandleErr` / retry budget / TSO recovery 相关路径,确认它是 retry 分类问题,还是 retry budget / stream lifecycle 问题；
   - 把 `txn` 绿 / `fast_reorg` 红 这组结果反投到 ingest/backfill owner 上,找最窄的 component owner。
3. 如果源码锚点清楚,再补一个更轻的 failpoint/UT 版本；live cluster probe 则作为外部强证据保留。

### 7m. 2026-07-10 fast-reorg `ADD INDEX` 的 PD TSO retry-timeout 被 DDL 误判成 fatal
**为什么 7l 还不够**:
- 7l 已经证明了 live RED,但当时还有三种可能没分开:
  1. checkpoint path 自身缺 retry；
  2. retry budget 太小；
  3. retry classifier 把本应可恢复的 PD 错误当成 fatal。
- 这轮补的是第 3 点的实锤。

**新的决定性证据**:
- live testbed `8220955` 同日复查:
  - `mysql.tidb_ddl_history` 的 job `1192` / `1204` 都显示:
    - `err.message='create TSO stream failed, retry timeout'`
    - `err.rfccode='PD:client:ErrClientCreateTSOStream'`
    - `err_count=1`
    - `is_fast_reorg=true`, `is_dist_reorg=false`
    - `state=3`
  - `ADMIN SHOW DDL JOBS` 对应终态都是 `rollback done`
  - 对照格 `fast_reorg=OFF` 的 job `1196` 为 `synced`,comment 明确走
    `txn, thread=1, batch_size=32, max_node_count=3`
- 本地最小 classifier probe:
  - `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go`
  - 当前代码对 raw/traced/stacked 的
    `ErrClientCreateTSOStream(... retry timeout)` 都打出:
    - `Unknown error class [class=PD]`
    - `isRetryableError(...)=false`
  - 也就是说 live 的 `err_count=1` 不是巧合,而是 classifier 本来就不把这类错误当
    retryable。

**源码链路**:
- `/Users/bba/pc/tidb/pkg/ddl/index.go`
  - `runIngestReorgJob` 只有 `isRetryableJobError(err, job.ErrorCount)` 为 true 时才会继续
    retry 路径。
- `/Users/bba/pc/tidb/pkg/util/dbterror/ddl_terror.go`
  - `ReorgRetryableErrCodes` / `reorgRetryableErrMsgs` 不包含 PD TSO stream retry-timeout 这个
    error family。
- `/Users/bba/pc/tidb/pkg/parser/terror/terror.go`
  - `type Error = errors.Error`;`terror.ToSQLError` 遇到未知 RFC class `PD` 会 fallback 到
    generic MySQL code。
- `/Users/bba/pc/tidb/pkg/ddl/ingest/checkpoint.go`
  - `afterImport` / `resumeOrInitCheckpoint` 会直接把 `pdCli.GetTS` 错误冒回上层。
- 合在一起就是:
  - foreign PD normalized error
  - -> 进入 `*terror.Error` 分支
  - -> unknown RFC class fallback
  - -> 不在 retryable code list
  - -> DDL 直接把 transient fault 当 fatal

**用户层表现**:
- 用户执行 online `ADD INDEX`,PD 短时重启两次后,即使集群已恢复 ready,DDL 仍直接失败:
  - `ERROR 1105 (HY000): create TSO stream failed, retry timeout`
- 从 job history 看,这不是“重试很多次后还是失败”,而是 **只打到一次就 rollback**。

**已入库**:
- 远端 `found_bug` 已入库为 `id1290001`
  - title:`Fast reorg ADD INDEX rolls back on transient PD TSO stream retry timeout instead of retrying`
  - severity:`high`
  - `root_cause_id=addindex-fastreorg-pd-tso-retry-misclassified-fatal`
- 草案: `/Users/bba/pc/ai-native-fast-reorg-pd-tso-retry-timeout-draft.md`

**方法论增量**:
- 新 selector:`S24 transient fault retry classifier`
- 新 oracle:`O31 DDL retryable fault terminal oracle`
- 核心不是继续加更多 topology chaos,而是先问:
  1. fault 有没有真的打进 active window；
  2. 它有没有真正进入 retry budget；
  3. `err_count=1` 这种 single-hit terminalization 能不能和 sibling green 控制一起把问题
     压缩到 classifier / error-domain bridge。

**暂停门**:
- 不要继续枚举更多 PD bounce 次数或 error string。
- 只在另一个 foreign error-domain retry classifier、silent data consequence、或 fix
  validation 时重开这条 lane。

### 7n. 2026-07-10 `issue62531` failpoint-pinned row-rewrite 子窗口负校准
**这轮补的不是更大的 workload,而是更窄的窗口**:
- 在 `/Users/bba/pc/tidb/pkg/ddl/column.go`
  的 `updateColumnWorker.BackfillData(...)` 末尾新增
  `afterUpdateColumnBackfillCommitted` failpoint:
  - 位置在单个 backfill txn 成功提交之后、
    `mockUpdateColumnWorkerStuck` 之前。
  - 目的不是造假红,而是把 delete/reinsert pin 到
    “一个 row-rewrite 小批次刚提交完”的 child window。
- 在 `/Users/bba/pc/tidb/pkg/ddl/modify_column_test.go`
  新增最小 probe:
  - `TestModifyColumnDeleteReinsertAfterBackfillCommitted`
  - shape=`id PK + val0 int not null + idx(val0)`，
    `ALTER TABLE ... MODIFY COLUMN val0 VARCHAR(8) NOT NULL`
  - `tidb_ddl_reorg_worker_cnt=1`
  - `tidb_ddl_reorg_batch_size=1`
  - 第一次 `afterUpdateColumnBackfillCommitted` 命中后,
    用第二个 session 做 `DELETE id=1` + `INSERT id=1,val0=4`
  - terminal oracle:
    - `ADMIN CHECK TABLE`
    - `SELECT * ORDER BY id`
    - `USE INDEX(idx)` vs table path
    - 额外 `BEGIN; DELETE ...; ROLLBACK` 触发 reader/DML 路径

**结果**:
- 本地 `go test --tags=intest ./pkg/ddl -run TestModifyColumnDeleteReinsertAfterBackfillCommitted -count=1`
  GREEN。
- failpoint 确认命中,说明这不是“窗口没打进去”的假绿。

**这条负样本的意义**:
- `issue62531` 这条 severe lane 在 current master 上又向里推了一层:
  - 不只是 broad workload 绿,
  - 不只是 full-reorg + concurrent DML + owner handoff 绿,
  - 现在连 “单 batch commit 之后立刻 same-key delete/reinsert”
    的本地 child window 也先是绿。
- 这说明下一步如果还追这条 lane,
  不该回去盲加规模,
  而应优先考虑:
  1. real TiKV reader path 特有的 row decode/oracle；
  2. 更尖的 rollback-resume / recovery split；
  3. changing-column / sibling path 的 mid-flight 恢复。

### 7o. 2026-07-10 non-partition add-index owner-fault harness 的 observer 修复与 live 绿样本
**先修 probe,再谈结果**:
- `/Users/bba/pc/ai-native-probes/add_index_owner_fault_oracle_probe.go`
  这轮不是只换参数,而是补了三个关键 observer 能力:
  1. `fast-reorg` flag:
     - 允许区分 fast ingest 快路径和更慢的 txn reorg 路径。
  2. phase gate:
     - 新增 `fault-min-running`
     - 新增 `fault-schema-state`
     - fault 只在目标 phase 持续一段时间后才允许触发。
  3. `job_id watermark` + subjob-aware lookup:
     - 同一条 multi-schema DDL 在 `information_schema.ddl_jobs`
       里会同时出现 parent job 和 `add index /* subjob */`
     - parent 行常常长期是 `schema_state=none,row_count=0`,
       真正的 active reorg 只体现在 subjob 行；
     - 重复跑同一条 SQL 时,还会被历史 job 污染。
     - 现在 lookup 会:
       - 只看 `job_id > watermark` 的当前轮次；
       - 优先选择 `add index` subjob；
       - 再用 `schema_state != none` / `row_count` 排序。

**修 probe 过程中抓到的两个 observer 债**:
- 旧 observer 会一直盯 parent multi-schema 行,
  误以为 job 一直停在 `schema_state=none`。
- 不加 watermark 时,`order by job_id desc limit 1` 仍可能被
  历史同 SQL job 污染,直接拿到旧成功/旧失败记录。

**live 校准(当前 testbed `8220955`)**:
- 运行参数:
  - `rows=400000`
  - `regions=160`
  - `ddl-shape=multi`
  - `dist-task=false`
  - `fast-reorg=false`
  - `fault-mode=delete-owner-pod`
  - `fault-count=1`
  - `fault-min-row-count=10000`
  - `fault-min-running=3s`
  - `fault-schema-state='write reorganization'`
  - `dml-workers=8`
  - `dml-slots=32`
- 关键 live 观察:
  - watermark=`1480`
  - 当前轮 job=`1484`
  - 在 `write reorganization` 活窗里连续观察了 5s,
    之后 `row_count=39386` 触发 owner fault
  - 真正 owner handoff:
    - `tc-tidb-1 -> tc-tidb-0`
  - DDL 在 fault 后继续跑了约 `1m38s`
  - terminal state=`done`
  - visible indexes=`[PRIMARY idx_d uk_c]`
  - `ADMIN CHECK TABLE` GREEN
  - `uk_c` / `idx_d` count 都是 `400000`
  - hot-slot oracle(32 个 delete/reinsert 槽位) GREEN

**结论**:
- 非 partition、单表、
  `ADD UNIQUE INDEX + ADD INDEX + concurrent delete/reinsert + mid-write-reorg owner handoff`
  在 current master 上是 GREEN。
- 但这轮不是“白跑”:
  - 我们把 severe live harness 从
    “会打到尾巴/会看错 parent/会吃历史 job”
    升级成了
    “能证明自己打进当前 subjob active window”的资产。
- 后续如果继续追 `OWNER_TOPOLOGY_HANDOFF`,
  应优先接:
  1. 同一个 live harness 上叠 `cancel` / `pause-resume`
     看 terminal state + schema/index visibility 是否破；
  2. foreign error-domain / retry-classifier 这类
     topology-adjacent single-hit terminalization；
  3. 只有在 observer 还能证明 active window 的前提下,
     才继续扩 fault schedule。

### 7p. 2026-07-10 add-index live harness 再进一层: control 面绿链 + fast-reorg 早窗校准
**这轮主要不是追新红,而是把 7o 的 harness 从“能打进 active window”继续推到“能稳定承载 control 面”**。

**probe 继续补强**:
- `/Users/bba/pc/ai-native-probes/add_index_owner_fault_oracle_probe.go`
  新增:
  - `control-mode={none,pause-resume,cancel,pause-resume-cancel}`
  - `control-delay`
  - `control-pause`
  - `control-min-row-count`
- 终态纪律修正:
  - `cancelling` 不再算 terminal。
  - 只接受 `done/synced/rollback done/cancelled` 作为真正终态。
- rollback 路径的 observer debt 修正:
  - hot-slot oracle 在索引不存在时不再强行 `USE INDEX(...)`。
  - 无 hint 路径的 SQL 拼接补了缺失空格。
- DML harness 再次吸收了前面 modify-column 的经验:
  - insert 路径遇到 `duplicate entry` 时,
    若语义上可能是“上一跳已成功但回包断了”,按 success 处理,避免 worker 自杀。
- pause/resume 的状态观察改成:
  - **同一 job_id 任一相关行**命中目标状态即可,
    不再死盯 preferred subjob 行。

**这轮抓到但先不升级为产品 bug 的 observer 事实**:
- multi-schema `ADD INDEX` 在 `ADMIN PAUSE DDL JOBS` 后,
  `information_schema.ddl_jobs` 里可能表现为:
  - parent job=`paused`
  - `add index /* subjob */` 仍显示 `running`
  - 但 `row_count` 稳定不再推进。
- 这更像状态可见面的语义差异/observer debt,
  目前没有直接用户级严重后果证据,
  先不按 severe bug 记。

**live 结果 1: txn reorg + owner handoff + cancel**:
- 参数:
  - `rows=400000`
  - `regions=160`
  - `dist-task=false`
  - `fast-reorg=false`
  - `control-mode=cancel`
  - `control-min-row-count=120000`
- 关键链路:
  - active window 命中 `job=1496`
  - owner handoff:`tc-tidb-0 -> tc-tidb-1`
  - cancel 在 `row_count=427888` 后触发
  - DDL session 返回用户级错误:
    - `Error 8214 (HY000): Cancelled DDL job`
  - terminal=`rollback done`
  - visible indexes=`[PRIMARY]`
  - row counts=`400000`
  - hot-slot oracle(32 slots) GREEN
- 结论:
  - **owner handoff × cancel** 在 current master 上是 GREEN,
    并且错误面回到了“正常 cancel”,没有再冒内部 rollback 错误。

**live 结果 2: txn reorg + owner handoff + pause-resume-cancel**:
- 参数:
  - 同上,但 `control-mode=pause-resume-cancel`
  - `control-pause=5s`
- 关键链路:
  - active window 命中 `job=1504`
  - owner handoff:`tc-tidb-0 -> tc-tidb-1`
  - `pause` 成功
  - `resume` 成功
  - post-resume 再 `cancel`
  - terminal=`rollback done`
  - visible indexes=`[PRIMARY]`
  - row counts=`400000`
  - hot-slot oracle(32 slots) GREEN
- 结论:
  - **owner handoff × pause-resume-cancel** 也没有打出新的终态/一致性红点。

**live 结果 3: fast-reorg 早窗 owner-fault 校准**:
- 参数:
  - `rows=1000000`
  - `regions=256`
  - `dist-task=false`
  - `fast-reorg=true`
  - `fault-min-row-count=0`
  - `fault-min-running=0s`
  - `dml-workers=16`
  - `dml-slots=64`
  - `control-mode=none`
- 关键链路:
  - 在 `row_count=0` 的 `write reorganization` 活窗里立即打 owner fault
  - owner handoff:`tc-tidb-1 -> tc-tidb-0`
  - DDL 仍成功收敛到 `done`
  - visible indexes=`[PRIMARY idx_d uk_c]`
  - `uk_c` / `idx_d` count=`1000000`
  - hot-slot oracle(64 slots) GREEN
- 结论:
  - fast-reorg 路径不是“根本打不进活窗”,只是窗口更短；
  - 只要把 phase gate 收紧到足够早,owner fault 仍能被真实注入。

**这轮方法论上的净收获**:
1. 同一个 severe live harness 已经能承载:
   - owner handoff
   - cancel
   - pause-resume
   - pause-resume-cancel
   - txn reorg / fast-reorg 两类路径
2. 对 fast path,`proof obligation` 不是“把 fault 打得更重”,
   而是**把 observer 打得更早、更准**。
3. 这也再次证明:
   - 先修 observer debt,
   - 再谈结果,
   往往比盲扩 matrix 更快逼近真正 severe 红格。

### 7q. 2026-07-10 `MODIFY COLUMN` owner->TiKV live 负样本:恢复性已证,语义级 retry bug 还要更近的注入
**这轮最重要的不是又打一轮网络故障,而是把“live 绿 / local 红”的分叉点钉住**。

**probe 补强**:
- `/Users/bba/pc/ai-native-probes/modify_column_owner_fault_oracle_probe.go`
  新增:
  - `network-target-component={tikv,tidb,pd}`
  - `network-partition` 在未显式给 `fault-pod(s)` 时,默认取**当前 DDL owner pod**
  - `NetworkChaos` 改成:
    - source selector = 选中的 owner pod
    - target selector = 指定 component(本轮默认 `tikv`)
- 本地 `gofmt` + `go build` 已通过。

**live 参数**:
- `rows=1000000`
- `fault-mode=network-partition`
- `network-target-component=tikv`
- `network-duration=10s`
- `fault-min-row-count=20000`
- `fault-min-running=3s`
- `fault-schema-state='write reorganization'`

**关键链路**:
- 预填充完成后,`ALTER TABLE ... CHANGE COLUMN x a VARCHAR(16) NOT NULL` 进入 `write reorganization`
- active fault window 命中:
  - `job_id=1516`
  - `row_count=40768`
- 注入:
  - current owner=`tc-tidb-0`
  - `NetworkChaos source=[tc-tidb-0] target_component=tikv duration=10s`
- 故障后现象:
  - `row_count` 先卡在 `86176`
  - 随后恢复推进:
    - `110528 -> 164544 -> ... -> 999890 -> 1999886`
- 终态:
  - DDL session success
  - job=`synced`
  - final oracle GREEN
  - `cols=[a y]`

**和 local 红样本的并置**:
- local 语义注入仍是 RED:
  - `/Users/bba/pc/tidb/pkg/ddl/ai_native_reorg_grpc_probe_test.go`
  - 单次 `mockBackfillRunGrpcUnavailable`:
    - `ADD INDEX` retry + PASS
    - `MODIFY COLUMN` fail + rollback
  - 单次 `mockBackfillRunGrpcDataLoss`:
    - `ADD INDEX` retry + PASS
    - `MODIFY COLUMN` fail + rollback
  - 这说明它不是只绑在 `Unavailable` 这个单点错误串上,
    而是至少覆盖到同一 transient family 里的另一个 end-to-end case
- source 侧分叉已经足够清楚:
  - `job_worker.go:toTError`
    - foreign/transient error 会被 synthesize 成 `CodeUnknown`
  - `index.go:isRetryableJobError`
    - 走 `isRetryableError(err, true)`
  - `modify_column.go:isRetryableModifyColumnReorgJobError`
    - 走 `isRetryableError(err, false)`
    - 注释直接说明它为了避免 deterministic conversion error 的长重试

**方法论升级**:
1. **粗粒度基础设施故障**(owner delete / pod bounce / owner->TiKV network partition)
   最适合验证:
   - `S-STATE`
   - `S-LIFE`
   - observer 是否真的打进 active window
   - system 是否具备恢复性
2. **bridge-proximal 语义故障**(单次 worker return / 单次 grpc unavailable / error bridge 前后的身份丢失)
   才适合验证:
   - `S-ERR`
   - `S-RETRY`
   - error identity 是否跨 `toTError` / persist / retry classifier 被保留
3. 因此:
   - **local semantic RED + live infra GREEN** 不应被当成假阳性
   - 也不该继续盲目加大网络故障矩阵
   - 下一步应转成**更近 error bridge 的注入**:
     - `BackfillData -> toTError -> isRetryableModifyColumnReorgJobError`

**下一步**:
- 如果目标是继续追这条 severe availability bug,更优先的不是再打一轮 owner->TiKV network chaos,
  而是建设一条更近语义桥的 harness/failpoint:
  - 单次 transient error 注入在 `BackfillData` 返回边界
  - 或 failpoint-enabled live lane,让 testbed 上也能打一次真正的 `grpc unavailable` 语义错误
- 这条结论本身就是 loop 的增量:
  - **恢复性验证**和**错误分类验证**必须分 lane,不能混用一个粗故障模型去证明两件事。

### 7r. 2026-07-10 `MODIFY COLUMN` transient connection family:不是单点 gRPC,而是一整个 fatal-family
**这轮把 7q 从“两三个点”继续推进成了一个更像产品 bug 的 error family**。

**probe 补强**:
- `/Users/bba/pc/tidb/pkg/ddl/backfilling.go`
  新增 failpoint:
  - `mockBackfillRunTransientErr`
  - 支持:
    - `mysql_invalid_conn`
    - `driver_bad_conn`
    - `net_conn_reset`
    - `net_broken_pipe`
    - `net_conn_refused`
- `/Users/bba/pc/tidb/pkg/ddl/ai_native_reorg_grpc_probe_test.go`
  新增:
  - `TestAINativeAddIndexRetriesTransientConnErrorFamilyProbe`
  - `TestAINativeModifyColumnFailsTransientConnErrorFamilyProbe`

**本地验证**:
- 运行:
  - `go test --tags=intest ./pkg/ddl -run 'TestAINative(AddIndexRetriesTransientConnErrorFamilyProbe|ModifyColumnFailsTransientConnErrorFamilyProbe)$' -v -count=1`
- 结果:
  - `ADD INDEX`:
    - 5 个 shape 全部 GREEN
    - 日志可见 `run DDL job failed, sleeps a while then retries it`
    - 之后收敛到 `State:synced`
  - `MODIFY COLUMN`:
    - 5 个 shape 全部 RED
    - 用户级错误直接暴露:
      - `invalid connection`
      - `driver: bad connection`
      - `connection reset by peer`
      - `broken pipe`
      - `connection refused`
    - 终态进入 `rollingback -> rollback done`

**这轮的价值**:
1. 这不再是 `grpc unavailable` / `grpc dataloss` 的单点现象。
2. 它已经扩成一个更接近真实线上瞬时故障的**连接类 transient family**。
3. 这让 bug 质量明显提升:
   - 更像真实用户会遇到的短暂 socket/network hiccup
   - blast radius 不只是一种内部错误码
   - sibling path(`ADD INDEX`)仍能作为强 GREEN 参考

**资产**:
- 新草稿:
  - `/Users/bba/pc/ai-native-modify-column-transient-rollback-draft.md`

**当前结论**:
- 如果要继续把这条 severe bug 往 issue/入库级别推进,
  最值得做的是:
  - live failpoint lane,或
  - 更直接的 issue/repro 整理,
  而不是再扩 owner/network 的粗故障矩阵。
- 对当前 testbed `8220955` 的补充校准:
  - status 口 `31188` 可达
  - 但即使给 TiDB pod 注入
    - `GO_FAILPOINTS=github.com/pingcap/tidb/pkg/server/enableTestAPI=return(true)`
    - 并等待 tidb 滚动重启
  - `/fail/` 仍然是 `404`
  - 启动日志里也没有 `enableTestAPI` / failpoint 相关告警
  - 说明当前 testbed 的 `tidb-server:master` 镜像**不是 failpoint-instrumented build**
  - 所以这条 severe family 想做 live semantic injection,
    需要:
    - failpoint build,或
    - 单独自定义镜像/部署 lane,
    不能直接靠现成 testbed 打开 `/fail/`

---

## 8. 关键坑/教训(汇总)
- **pod 是 busybox**:无 tar(`kubectl cp` 不可用,用 `kubectl exec -i ... 'busybox gunzip -c >file'` 流式塞);`pkill -f` 不按 cmdline 匹配(按 `argv[0]=="/fp"` 杀);kill 循环别用进程 cmdline 里出现的串匹配(会杀执行脚本自身)。
- **端口 flag 是 `-P` 不是 `--port`**;状态口 `--status`。
- **DDL backfill 跑在 owner** → fp-tidb 必须是 owner(已缩 managed=0)。
- **managed=0 → NodePort 无后端**,SQL 必须走 port-forward(笔记本休眠会断,需重建)。
- **metamorphic/TLP 误报源**:系统表(information_schema)/非确定查询 → triage 门只信稳定用户表上的差异。
- **TRUE-positive demo 的三个假阴性坑**:见 §6(fast_reorg 必须 ON、id 区间每轮全新、ignoreReadIndexDupKey 无效)。
- 跑大量建删表要确保 `tidb_gc_enable=ON`。
- `ddl_scenario` 的 sid 不能依赖自增,用 Python 显式分配。
- 挖掘抽取:子代理输出 JSON → 脚本(`extract.py`)读 task .output 抽最后一个 ```json 块,不把大输出读进上下文。
- CTE 造数据默认 `cte_max_recursion_depth=1000`,要 `SET SESSION cte_max_recursion_depth=200000`(或用 doubling)。

**2026-07-12 `ADMIN REPAIR TABLE` index metadata selector 已完成 contract screen: INVALID,不计 bug。**
这轮没有继续枚举 multi-schema 的 TODO:普通表上 `ADD COLUMN + ADD UNIQUE INDEX` 的自然重复键
失败正常 rollback,列和索引都没有残留;因此它是 negative evidence。`REPAIR_INDEX_PHYSICAL_METADATA_RECONCILIATION`
确实能打出强 wrong-result:物理 `KEY idx_v(v(3))` 修复成 `v(2)` 后 table scan 找到
`abc-two`,`FORCE INDEX` 找不到;物理普通 KEY 修复成 UNIQUE 后默认 `Point_Get` 只返回 `id=4`,
table scan 仍返回 `1,3,4`,重复写入也成功,`ADMIN CHECK TABLE` 静默。

但官方 TiDB ADMIN 文档明确把 repair 定义为 untrusted,要求 operator 手工确保原始 metadata
被 supplied `CREATE TABLE` 准确覆盖。因此这些 mismatch 是故意违反 contract 的输入,不是可提的
产品 bug。已将 target 标为 `retired`,运行标为 `INVALID(contract-untrusted-repair-definition)`,
保留 selector/oracle 作为 recovery guardrail。证据仍在
`assets/store/logs/admin-repair-index-metadata-red-20260712.log`,资产提交为 `6a770cd`。

**2026-07-12 optimistic retry state-replay screen: retired, not a new bug.** A local strong probe forced one optimistic commit retry after `SELECT @retry_value := v` and changed the source from 10 to 20 before retry; the final write still used 10, and the retry log showed only the write history. TiDB's official optimistic-transaction contract documents write-only replay, warns that query-derived writes may violate Repeatable Read, and deprecates automatic optimistic retry from v8.0. The candidate is therefore `known/documented-semantic-boundary`, excluded from the bug count. The attempted testbed lift was `INVALID(failpoint-lifecycle)` because this dirty failpoint binary left an empty action after HTTP DELETE and panicked at `val.(bool)`; it is not product evidence. Asset: `docs/method-cases/ai-native-txn-retry-user-variable-known-boundary.md`。

**2026-07-12 txn/state-ingress live lift: `RECOMMEND INDEX RUN` 已在 testbed 8220955 打出无 failpoint 的 wrong-snapshot RED。** 这轮先修正了环境入口: `tidbbug` login path 指向另一套 TiDB,所以所有 live SQL 均显式走 `127.0.0.1:14000`。在同一数据库里先提交 `(1,10)`,记录 `2026-07-12 12:58:03.379233`,等待 2 秒后提交 `(2,20)`。直接 `SET TRANSACTION READ ONLY AS OF TIMESTAMP ...; SELECT` 只返回 `(1,10)`；同样的 stale setup 后执行成功的 `RECOMMEND INDEX RUN FOR 'SELECT ...'`,下一条用户 `SELECT` 返回 `(1,10),(2,20)`；无 pending stale state 的 advisor control 正常返回两行。证据:`assets/store/logs/txn-index-advisor-txreadts-testbed8220955-20260712.log` 和 `assets/store/txn-index-advisor-txreadts-testbed-results.jsonl`;草案:`docs/bug-drafts/ai-native-index-advisor-txreadts-id1260002-draft.md`。远端 bug 库 `id1260002` 已补入 testbed repro/actual/evidence,仍保持 `medium / contract-needed / confirmed=0`,不能因为 live RED 就越过产品契约门。当前判定:源码路径、用户可观察 wrong snapshot、无 failpoint live RED、无 pending control、local ingress-isolation GREEN 都已成立；唯一未闭合的是 TiDB 对 `SET TRANSACTION` 中“next query statement”的边界是否把 management statement 内部 helper SQL 视为用户的 next query。**

**2026-07-13 新 high-severity 命中 id1620002: TTL job 内 scan/delete 时区上下文漂移会静默删除已刷新的 DATETIME 行。** 该候选完全从当前源码证明义务产生,没有使用 PR/review finding 选题:`SQLBuilder` 让 scan/delete 都携带同一 epoch `E`,但每条 TTL SQL 独立 reset 到最新 global `time_zone` 后执行 `FROM_UNIXTIME(E)`；`validateTTLWork` 不检查时区。testbed 8220955 的真实 worker 在 UTC scan 后暂停,把已选中行刷新到原 cutoff+4h(原谓词=0),切 global time_zone 到 +08 后释放 delete；同一 epoch 的 cutoff 扩大 8h,行被删除且 job 正常完成。相同 pause/refresh 但 UTC 不变时行保留。命中后去重才发现历史 #41043/#41044；其“job 启动前切时区”原场景在当前源码为 GREEN(id 1/2 保留),所以本次是不同的 mid-job context-stability 缺口。新 selector `SCAN_DELETE_CONTEXT_STABILITY`:跨阶段携带 token 时,必须同时证明解释 token 的语义上下文被 pin/version/revalidate；存在 recheck 不等于 recheck 含义稳定。远端 `found_bug` 已入库 `id1620002/high/confirmed`,当前 93 行/70 roots。资产:`docs/bug-drafts/ai-native-ttl-midjob-timezone-drift-refreshed-row-draft.md`,`docs/method-cases/ai-native-ttl-context-stability-method-case.md`,`assets/store/ttl-midjob-timezone-drift-results.jsonl`;证据:`context logs/0027-ttl-worker-timezone-drift-red.log`。暂停门:不枚举 offset/interval/batch/DATE 变体。

**2026-07-13 current-source-only terminal boundary lane 命中 id1770003/high。** 本轮没有使用 PR
review finding 生成候选。`pkg/executor/importer.ProcessChunk` 的 data/index writer `Close` 都在
defer 中执行,错误只写日志,函数仍返回更早的 nil;而 `ingestctrl.Writer.Close` 正是 private KV
buffer 的最终 flush,失败后 buffer 被销毁。local mock RED 先证明两个 Close sentinel 都被吞,
named-return+multierr 单变量反事实转 GREEN。随后在授权 testbed 8220955 用 file IMPORT INTO、
`THREAD=1,CHECKSUM_TABLE='off'` 做真实提升:current source 的 job `finished`/exit 0/
Imported_Rows=3,但 table/index=3/0,`ADMIN CHECK TABLE` 报 8223;无故障为 3/3+ADMIN GREEN;
同高度 named-return 反事实为 ERROR 1105、0/0+ADMIN GREEN。去重只在 RED 后进行:#69756/
id1260008 是 visible data-close error 后跳过 sibling Close,id1590002 是 visible engine error 前已
发布 data,均不是本 root 的 false success。远端 `found_bug id1770003`,当前 98 rows/75 roots/
23 high。新 selector `DEFERRED_TERMINAL_ERROR_DOMINATES_SUCCESS`,oracle O41。资产库 212
revisions,C3_DIRECT=14,RED=39/GREEN=34,next=null。暂停门:不枚举 writer/error 类型;只在不同
public terminal owner 或 fix validation 时重开。

**2026-07-13 selector 复用命中 id1800003/high:取消 ALTER TABLE PLACEMENT 后 PD 保留未提交的
副本规则。** 本轮明确不使用 PR review finding。候选由当前源码和已验证 S35 直接生成:
`onAlterTablePlacement` 先在 DDL txn 中 stage 新 policy ref,再通过 `context.TODO()` 把 bundle
发布给 PD,最后才完成 job/txn；generic cancel 没有 compensation。local mock-PD RED:ALTER 8214,
metadata p1/r1,PD p2/r2；正常 ALTER 和 committed-bundle republication 均 GREEN。授权 testbed
8220955 的 real-PD RED 使用合法副本数而非不存在的 region label:p1 `FOLLOWERS=2`(3 voters),p2
`FOLLOWERS=1`(2 voters)。job 5369 被 ADMIN CANCEL 后 history=cancelled、SHOW CREATE 仍是 p1,
但 PD `TiDB_DDL_5367` 保留 count=2；正常 job 5372 的 metadata/PD 都是 p2/count=2。用户后果是
取消成功却静默降低声明的副本冗余。RED 后才做资产/upstream issue 去重,无 exact root。资产新增
O42、owner-specific obligation/scenario/fault/fixture；S35 本身复用,不新造 selector。方法论增量:
复用 selector,不复用 finding；每个新 handler 仍须独立重建 P/Q/F、durable owner 和强 oracle。
已按完整 UT 复现提交 upstream issue #69784,标签 `found-by-ai`,`component/ddl`,`severity/major`；
远端 `found_bug id1800003` 已同步为 `issue-filed`。

**2026-07-13 S35 consumer-altitude 命中 id1830003/high,upstream #69785。** 当前源码
`onSetTableFlashReplica` 在更新 `TiFlashReplicaInfo` 和完成 DDL job 前调用
`ConfigureTiFlashPDForTable`;count=0 会直接删除 `tiflash/table-<id>-r`。local mock RED:job 120
cancelled/8214,metadata count=1 available=true,PD rule absent；normal removal 与 committed-metadata
republication GREEN。为了不靠推断定 severity,testbed 8220955 临时部署了兼容的
`v9.0.0-beta.2.pre-177` TiFlash。表 5378 的 TiFlash-only query 先 GREEN(5/150)；PD rule 删除后
ADMIN CANCEL job 5382,metadata 仍 count=1/available=1,PD rule absent,同一 MPP query 返回 9012
TiFlash server timeout。只恢复 captured committed rule 后 progress=1/query=5/150；正常 job 5383
删除 metadata/PD 后,TiFlash-only session 立即返回明确 1815。RED 后 exact issue 搜索为空,已入库
id1830003 并提交 #69785。方法论增量:control-plane drift 必须继续追到 consumer altitude；owner
split 用来 admission,真实 consumer consequence 用来定 severity。发现过程没有使用 PR review finding。

**2026-07-13 current-source-only 命中 id1860003/high:CRR resume state 可跨 replication lineage
抬高 PITR 可恢复点。** `PersistentState` 只保存 LastCheckpoint/SyncedTS/SyncedByStore,固定写到
downstream 的 `crr-checkpoint/resume-state.json`,没有 upstream cluster、task generation 或 storage
identity。lineage A 留下 100 后,同 task 名/同 bucket 的 lineage B 当前 checkpoint=10；calculator
直接返回 100 且 object checker 0 次调用。restore consumer 同样优先 resume 100 而不是 storage
checkpoint 10；未显式指定 restore-ts 时还会把 100 设成默认目标。同-lineage 100/100 与 no-state
current=10 均 GREEN。RED 后四组 GitHub issue 搜索无 exact root。远端 `found_bug id1860003`,当前
101 rows/78 roots/26 high。新 selector S39 `PERSISTED_STATE_MUST_BIND_LINEAGE`,oracle O44。发现
过程没有使用 PR review finding,testbed 未使用。

**2026-07-13 S39 第二个 current-source-only 命中 id1890003/high:Lightning importinto 会用旧
Finished checkpoint 静默跳过新输入。** TableCheckpoint 按 table name 查找,虽保存 JobID/Status/
GroupKey,但没有 input/config/target fingerprint；Finished 分支直接 return。完整 importer 还会把旧
GroupKey 复制进新运行,所以最初的 cross-group 矩阵被源码否决,改为保持 table/path/group 不变、只把
输入换成非空 `new-lineage.csv`。RED:`SubmitAndWait=nil,SubmitTable=0`；no-checkpoint control
`SubmitTable=1`,finished-resume baseline GREEN。`keep-after-success=rename` 是支持配置,但该 backend
成功后只实现 remove,保留状态仍在原查找位置。RED 后四组 GitHub issue 搜索无 exact root。远端
`found_bug id1890003`,当前 102 rows/79 roots/27 high；资产 231 revisions,RED=46/GREEN=43,
C3_DIRECT=18,next=null。发现过程没有使用 PR review finding,testbed 未使用。

**2026-07-13 S39 第三个 current-source-only 命中 id1920003/high:BR backup checkpoint 未绑定
source cluster,可把旧集群 SST 发布进新 backupmeta。** `CheckpointMetadataForBackup` 保存 config
hash/BackupTS,但没有实际 PD cluster ID；hash 只含 PD 地址字符串且故意不含 BackupTS。local consumer
RED 用 current client clusterID=222 加旧 completed range:`CheckCheckpoint=nil`,请求 TS 200 被旧 TS
100 覆盖,当前 incomplete ranges=0,最终 backupmeta 含 `old-cluster.sst`。no-checkpoint control 保留一个
current range；临时只加 `ClusterID=111` 并在入口比较 222 后,在 artifact publication 前失败关闭。
RED 后本地资产与四组 GitHub issue 搜索均无 exact root。远端 `found_bug id1920003`,当前 103 rows/
80 roots/28 high；资产 237 revisions,RED=47/GREEN=45,C3_DIRECT=19,next=null,pack open_gaps=[]。
发现过程没有使用 PR review finding,testbed 未使用。方法增量:缺 lineage field 只够产生候选,必须继续
验证“跳过当前 work + 发布旧 lineage artifact”；同时 obligation 必须通过 typed links 连接 selector/
oracle/scenario/schedule/fault,否则数据库无法支持下一轮增量执行。

**2026-07-13 current-source-only savepoint retry 候选退休为 GREEN。** 本轮没有使用 PR
review finding、issue 或历史修复生成候选。源码字段盘点发现 `ROLLBACK TO SAVEPOINT` 不会
snapshot/truncate `StmtHistory`,初看可能让已回滚 INSERT 在透明重试中复活。隔离 worktree 中按
TiDB 方式编译 failpoint,注入 retryability 并让 commit 恰好失败一次；trigger 证据为
`CouldRetry=true`,`ExecRetryCount=1`。实际 replay 顺序是 BEGIN、SAVEPOINT、INSERT(1)、
ROLLBACK TO、INSERT(2)、COMMIT；补偿动作也被重放,最终 retry/no-retry 都只有 `(2,20)`。
因此不是 bug,不入远端 bug 库,也未使用 testbed。新 selector/gate:
`REPLAY_COMPENSATION_CLOSURE`。发现 checkpoint 字段缺失后,必须继续计算“restore + forward
events + compensation events”的完整 effect closure；只有补偿事件缺失、乱序或换了语义 owner
才允许 C3 admission。私有资产库当前 243 revisions,RED=47/GREEN=47,C3_DIRECT=19,
`next=null`。

**2026-07-13 current-source-only 命中 id1950003/high:经典 Lightning checkpoint 未绑定 target
table generation。** 候选不是 PR/review finding 生成的。源码中 MySQL checkpoint 的 `hash`
实际写常量 `CheckpointStatusLoaded(30)`,file driver 则留有 `TODO check if hash matches`,同名项
会保留旧 TableID/status/engines/chunks。local RED 两格控制通过,跨 generation 得到旧
`101/Analyzed/2 engines`,而不是当前 `202/Loaded/0`。testbed 8220955 用 current commit 和支持的
`keep-after-success=origin`:第一次表 ID 5412 有两行；重建为 ID 5415 空表后,第二次 Lightning
exit 0,当前表仍为空,ADMIN CHECK 仅证明空表内部一致。TableID-only 反事实 live 无效,因为 TiDB
backend 的 checkpoint/current owner 都折叠为 0。上游 issue 搜索在 RED 后进行,无 exact root。
远端 `found_bug` 当前 104 rows/81 roots/29 high；私有资产 249 revisions,RED=49/GREEN=48,
INVALID=7,C3_DIRECT=20,pack open_gaps=[]。新方法增量:持久化 lineage 必须拆分 source、target
generation、config、artifact 四个 owner,并验证 identity field 在每个 backend 真实 materialize。

**2026-07-13 current-source-only partial-index pushdown 候选退休为 GREEN。** Fast ADD INDEX 在
完整 condition 可下推且只构建一个 index 时跳过 TiDB 本地 checker。候选 P/Q/F 完全来自当前
源码，没有使用 PR/review/issue/history。第一版 `CONNECTION_ID()` reference 被 constant-fold 后
仍下推，判 INVALID；修正为带行级 side effect 的 `LAST_INSERT_ID(id)` wrapper 后，计划明确形成
`cop[tikv] Selection` 对 root TiDB `Selection`。当前 grammar 的 15 个 signed/unsigned、ENUM/SET、
collation/PAD SPACE、decimal/float、DST/time、BIT/YEAR 边界全部 rowset 相同，因此不允许继续升到
ADD INDEX 或 testbed。新方法资产 S40 `PUSHDOWN_EQUIVALENCE_DOMINATES_RECHECK_ELISION`：pushable
只证明能力；跳过本地语义 owner 前必须先构造 root-only equivalent oracle 并验证 owner altitude。
资产库随后为 253 revisions，目标终态，禁止靠增加 literals 重开。

**2026-07-13 current-source-only TRUNCATE affinity 得到 moderate RED，并纠正严重性准入。** S35
从 `onTruncateTable` 找到 external owner 顺序。前三次使用现有 failpoint 的实验均 INVALID：源码
确认该点在 affinity create/delete 之前。隔离 worktree 增加精确 post-affinity、pre-schema-version
注入后，job 119 经支持的 ADMIN CANCEL 终止；InfoSchema 仍为旧 TableID 116 且声明
`AFFINITY='table'`，PD 旧组 `_tidb_t_116` 已消失。正常 TRUNCATE GREEN；仅按 committed TableInfo
重建 group 后 owner coherence GREEN。这个是真 bug，但官方承诺是实验性的 Region colocation/
query-latency 优化，不是数据正确性、副本安全或 required-path availability，因此从错误的
`C3_DIRECT` 修正为 `NOT_ADMITTED/moderate`，不做 testbed lift、不进入严重 bug 库。新增 LOOP gate:
故障注入前先把 external owner 追到最高 consumer，并用当前官方用户承诺校准 consequence；selector
强度不能继承此前 finding 的严重性。私有资产 258 revisions，RED=50/GREEN=51/INVALID=10，目标
retired=22，pack `open_gaps=[]`，`store.py next=null`。下一步只允许从当前源码产生新 C3 候选，
PR/review finding、issue 和 fix history 继续禁止作为生成器。

**2026-07-13 current-source-only 命中 id1980003/high：FLASHBACK CLUSTER 恢复 cached TableInfo，
却排除其必需的 `mysql.table_cache_meta` side row。** 候选从 `getFlashbackKeyRanges` 的恢复域与
cached-table DML consumer 对账产生，没有使用 PR review finding、issue、修复历史或 partition
路径。local `-tags=intest` consumer 矩阵先证明：缺 row 时 SELECT 回退正常，INSERT 在 commit 前
报 `table_cache_meta tid not exist`，仅补 row 后写恢复。随后 testbed `8220955` 在 commit
`5c9198e948` 做无 failpoint SQL-only 实锤：CACHE 表 ID 5428，target TSO
`467640514747564034`；NOCACHE 删除 row 后，FLASHBACK job 5432 `synced/public`，SHOW CREATE 恢复
`CACHED ON`，row count 仍为 0；SELECT 返回 `(1,10)`，INSERT `(2,20)` 报 ERROR 1105，数据未写入；
只补 `(5428,'NONE',0,0)` 后同一 INSERT 成功。环境已 NOCACHE、drop 测试库并恢复 TTL=ON。
新 selector S41 `RESTORE_DOMAIN_COVERS_RUNTIME_DEPENDENCIES`：恢复必须覆盖被恢复 capability 的
mandatory runtime dependency closure，或在发布前重建/对账。同期 FLASHBACK split 候选被校准为
NOT_ADMITTED：mock 删除了 client-go 内层 backoff，且官方契约明确不可取消、持续重试。RED 后六组
open/closed GitHub issue 搜索无 exact root。资产入口：
`assets/store/ddl-flashback-cache-side-state-results.jsonl`、
`docs/bug-drafts/ai-native-flashback-cluster-cache-side-row-draft.md`、
`docs/method-cases/ai-native-id1980003-flashback-runtime-dependency-method-case.md`、
`assets/store/logs/0084-flashback-cluster-cache-side-row-red.log`。

**2026-07-13 current-source-only 命中 id2010003/high：COM_STMT_PREPARE dedup 在清空
`tidb_read_staleness` 后仍复用旧 snapshot evaluator。** 候选由 fast-path P/Q/F 证明义务产生，
PR review、issue、fix/history 在独立 local RED 前完全禁用。`PrepareDedupCacheKey` 绑定 SQL、
charset/collation、DB、SQL mode 和 schema version，但 `rebuildFromPrepareCache` fresh Preprocess
之后仍用 `cached.Stmt.SnapshotTSEvaluator` 覆盖当前 `ret.SnapshotTSEvaluator`；旧 evaluator 捕获
了 prepare 时的 `ReadStaleness=-1s`。local 矩阵：先在 `-1` 下 warm，同 session 清空变量并把
`v=1` 更新为 `2`，相同 SQL 的新 prepared statement 返回旧值 `1`；仅关闭 dedup fast path 的
同 SQL 返回 `2`；只把 evaluator 改为 fresh owner 后全绿。testbed `8220955` 用单物理连接、真实
go-sql-driver `COM_STMT_PREPARE` 无 failpoint 复现 `dedup_on=1 / dedup_off=2`。官方 stale-read
契约明确清空变量后读取最新数据；dedup 开关默认 OFF 限制 reachability，但不降低 silent wrong
result consequence。post-RED 本地资产和三组 upstream issue 搜索无 exact root。远端 `found_bug`
现为 106 rows/83 roots，id2010003 confirmed high；私有库 272 revisions，RED=54/GREEN=53、
C3_DIRECT=21。新增 S42 `DERIVED_EXECUTION_CONTEXT_MUST_BE_KEYED_OR_REBUILT` 与 O52：cache 审计
必须盘点 derived field 的所有语义 producer；若 hit path fresh 分析后又被 cached derivative 覆盖，
该 discarded fresh value 是优先级很高的候选和精确反事实。资产入口：
`assets/store/prepare-dedup-stale-read-context-results.jsonl`、
`docs/bug-drafts/ai-native-prepare-dedup-stale-read-context-leak-draft.md`、
`docs/method-cases/ai-native-prepare-dedup-context-method-case.md`、
`scaffolds/go-probes/prepare_dedup_staleness_repro.go`。

**2026-07-13 current-source-only 命中 id2040003/high：distributed ADD INDEX 在后续批次 TSO
错误时可发布 partial index。** 候选从 `generatePlanForPhysicalTable` 的重试证明义务独立产生，未用
PR review、issue 或历史 finding 选题；这些来源只在本地和 testbed RED 后用于去重，未发现 exact
root。`subTaskMetas` 在 `RunWithRetry` 外声明；第二批 `allocNewTS` 失败后源码返回 `true,nil`，所以
两批源范围只发布一条 meta。只改成返回真实错误仍会把完整重试追加到失败前缀，得到三条 meta；
同时传播错误并按 attempt 清空 payload 才得到两条。testbed 8220955 使用真实 PD/TiKV/DXF，101
regions/100 batch/two TiDB executors：DDL job 5456 `synced`，table scan 为 `1,100999`，FORCE INDEX
只有 `1`，tail count `1/0`，ADMIN CHECK 返回 8223。远端 `found_bug` 已入库 id2040003，当前
107 rows/84 roots/32 high；私有资产 279 revisions，RED=57/GREEN=55。新增 S43
`RETRY_ATTEMPT_DERIVED_PAYLOAD_ATOMICITY` 与 O53：重试 closure 的 captured publishable state 必须
attempt-local/reset，成功后验必须证明 source domain exactly-once coverage，不能只看 nil error 或
nonempty payload。暂停本 root 的 batch/error/region 变体；下一轮继续从当前源码独立生成新的 C3
候选，PR review finding 继续禁止作为生成器。上游已提交
https://github.com/pingcap/tidb/issues/69789，状态同步为 `issue-filed`。

**2026-07-13 current-source-only 命中 id2070003/high：alternative logical plan 会把非空的聚合
IN 子查询变成 TableDual，并静默返回空结果。** 候选来自当前源码 clone/shortcut 证明义务，生成、
排序和本地 RED/GREEN 全程未使用 PR review、issue、fix 或历史 finding。`cloneDataSource` 为隔离 Join
与 Apply，分别 deep-clone `AllPossibleAccessPaths` 和 `PossibleAccessPaths`；但这两个 slice 原本是同一
批 mutable AccessPath 的 canonical/active 视图。stats 只填 canonical clone 的 ranges，physical
planning 却消费另一批 active clone。普通 correlated IN 会触发 leaf rebuild，因而掩盖缺陷；聚合把
correlation 留在 HashAgg 之上，leaf 不重建，active ranges 为空并被当成 TableDual。local 九格只有
aggregate IN 一格 RED：OFF=`1,2,3`，ON=空；只把 active path 映射到对应 canonical clone 后，仍选
Apply、恢复 real scan，九格全绿。testbed 8220955 在默认 cost、真实 TiKV、无 fault injection 下复现
相同 SQL-only RED；plain IN 控制在 ON/OFF 都返回 `1,2,3`。post-RED 去重无 exact root。远端
`found_bug` 为 108 rows/85 roots/33 high，id2070003 已 issue-filed；上游 issue
https://github.com/pingcap/tidb/issues/69790 带 `found-by-ai`/`severity/major`。私有资产 287
revisions，RED=59/GREEN=57，新增 S44 `CLONED_CANONICAL_ACTIVE_VIEW_IDENTITY` 与 O54，pack
`open_gaps=[]`。暂停 aggregate/index/cost 变体；下一轮继续只从当前源码 proof obligation 生成新 C3，
PR review finding 仍只能在独立 RED 后去重。

**2026-07-13 current-source-only 命中 id2100003/high：悲观 DML 自动重试会继承失败尝试的
SETVAR 副作用，把本应出现的重复键错误变成成功并写入另一行像。** 候选从当前源码 retry acceptance、
rollback owner 和 rebuilt-executor consumer 的差集产生；PR review finding、issue、fix/history 在独立
RED 前全部禁用。local 矩阵：无冲突 1/1，SETVAR 前冲突 1/1，SETVAR 后冲突 2/2，幂等 `:=7` 为 7/7；
自然 unistore 唯一键竞争中，并发会话提交 `(2,1)` 后，UPDATE 没有报重复键，反而提交 `(1,2)`。
只在接受 retry 前恢复 statement-entry UserVars，两个 RED 同时转绿。testbed 8220955 用纯 SQL
`SETVAR+SLEEP` 和真实 TiKV 无注入复现：A 成功、`@x=2`、rows=`(1,2),(2,1)`；无冲突控制为
`@x=1,(1,1)`，测试库已删除。post-RED 去重无 exact root。远端 `found_bug` 为 109 rows/86 roots/34
high；上游 https://github.com/pingcap/tidb/issues/69791 带 `found-by-ai`/`severity/major`。私有资产
295 revisions，RED=62/GREEN=60/INVALID=11，新增 S45/O55，pack `open_gaps=[]`。新方法：对每个 retry
点计算 `(pre-error mutations intersect post-reentry consumers) minus rollback owners`，先做 pre/post
altitude 小矩阵，再用自然 owner 和 terminal-error+durable-state oracle 升级；不证明落点的 timing 控制
必须记 INVALID。暂停本 root 的 DML/变量/索引/冲突时序变体，下一轮仍只从当前源码生成新的 C3。

**2026-07-13 current-source-only 命中 id2130003/high：IMPORT INTO 冲突删除事务的 Commit
错误被 defer 写进非返回槽，任务假成功并留下物理索引不一致。** 本轮候选由当前源码 retry closure
scanner 产生，PR review finding、issue、fix/history 在独立 local RED 前全部禁用。先用源码证明淘汰
两个高分误报：auto-ID allocator 每次从 authoritative global end 覆盖 captured outputs；nonpartition
DDL backfill 每次重置 row/index buffers 和 counters、重放 fixed range，outer frontier 只在成功后推进。
随后 `deleteBufferedKeys` 命中更强 P/Q/F：函数返回 unnamed `error`，`return nil` 在 defer 前固定
返回槽，defer 中 `err=txn.Commit(ctx)` 只改 local err；`RunWithRetry` 因此看见 nil 并跳过 retry。

local wrapper 明确 rollback 并返回 Commit error，当前函数仍返回 nil；只把 result 改成 named `err`
即转绿。testbed 8220955 用临时 MinIO/global sort、真实 PD/三 TiKV 和一次性 retryable pre-Commit
fault 做严格矩阵：current job 180001 报 `finished/Imported_Rows=1/3 conflicted rows`，但 PRIMARY/unique/
secondary 行集为 `2/1/2`，冲突行 id=2 的 record 与 secondary iv 存在、unique u 缺失，ADMIN CHECK
报 8223；同进程 fault-consumed control job 180002 为 `1/1/1 + ADMIN green`；named-return job 150001
记录一次 `retry-count=0` 后完成，仍为 `1/1/1 + ADMIN green`。所有实验 DB/MinIO/binary/process 已清理，
TidbCluster `tc` 恢复 `replicas=1/readyReplicas=1`；INVALID 遗留 job 90002 已通过恢复原 executor ID
完成 reversion 并处于 cancelled。

新增 S46 `DEFERRED_TERMINAL_ERROR_RETURN_SLOT_OWNERSHIP`：terminal action 被调用、error 被赋值都
不足以证明错误已处理；必须解析 defer 实际写入的 return slot，包括 unnamed result 和 shadowing。
scanner 同时增加两条降噪规则：remote-dominant overwrite-only output、attempt-entry reset + fixed-source
replay。远端 `found_bug` 已入库 id2130003，现为 110 rows/87 roots/35 high；上游
https://github.com/pingcap/tidb/issues/69792 带 `found-by-ai`/`severity/major`。私有资产 303 revisions，
RED=64/GREEN=62/INVALID=12，C3_DIRECT=22，`next=null`。暂停本 root 的 conflict/input/index/error
变体；下一轮继续只从当前源码产生新的 C3 候选。

**2026-07-13 txn current-source screen 与 L2 环境门更新：没有新 bug，新增 3 个负资产和一套
可重复本地运行链。** 本轮继续保持 COLD_SOURCE、非 partition、testbed 先关闭。先后审计
pipelined DML stale broadcast commitTS、shared-lock RPC envelope/resume、aggressive/fair lock 入口、
TiKV async prewrite apply。它们分别被最高消费者不使用精确 commitTS、client-go 展开 multi-owner、
入口显式拒绝、scheduler latch 覆盖 apply 等 guard 淘汰，没有把字段差异硬升为 bug。最强候选被压成
FK 最高消费者矩阵：两个 child insert 同时持有 parent shared lock，parent DELETE 等待；holder1
rollback 后仍须等待 holder2；holder2 rollback 控制要求 DELETE 成功，holder2 commit 目标要求
DELETE 重试 FK 检查并失败。矩阵两格 GREEN，parent/child 与 ADMIN CHECK 都符合预期。

第一次运行使用本机缓存的 `tiup nightly` TiKV `2d4737d`（build 2026-01-19），连仓库已有
`TestSharedLockBlockExclusiveLock` 都阻塞，所以严格记为 `INVALID(environment)`。删除并重装 nightly 后
得到 `7ecce12`（build 2026-07-13），官方 capability baseline 与新 FK 矩阵都通过；未触碰 testbed。
`tools/txnlab/local.py` 新增 `refresh-nightly/local-build/local-verify/realtikvtest`：nightly lane 记录实际
commit 且只声称 current-head capability，exact lane 校验 TiKV 二进制 SHA，runner 拒绝复用 2379、
保存日志并清理自己启动的进程。19 个 txnlab 单测与端到端 nightly baseline 均通过。新 scaffold 是
`scaffolds/tidb-tests/txn_shared_lock_parent_delete_revalidation_test.go`；资产 key 包括
`negative.txn-shared-lock-parent-delete-revalidation.v1`、
`negative.txn-pipelined-stale-broadcast-commit-ts.v1`、
`negative.txn-async-apply-latch-ordering.v1`、
`schedule.txn-feature-capability-before-candidate.v1`、`module.txnlab-local-runtime.v1`，均已导入
`assets.sqlite3`。方法论新增两条硬门：候选前先过最高消费者+入口/capability；AI source scout 同时
限制 command/source-region/token/wall-clock，不能再用“命令数少”伪装成低成本探索。

**2026-07-13 txn bounded source-packet checkpoint：仍无新 severe bug，但事务 proof debt 与 AI
探索预算进一步闭合。** 当前 source pin 不变：TiDB `5c9198e9484d`、client-go `661db4f5f4e8`、TiKV
`bf73df27b967`。本轮未触碰 testbed。lock resolver 终态缓存、lost `LockedWithConflictTS`、pipelined
exclusive-end、fair-lock delayed rollback、primary Region relocation 均完成最高消费者审计；分别由
durable status classification、TTL/heartbeat、read+GC recovery、旧/新 `forUpdateTS` 分代、按 key 重新
标 primary 等 owner guard 淘汰。它们应作为 negative screen 保留，不再重复。

新增 `tools/txnlab source-packet-scout`：parent 先选证明义务和精确源码区段，child 只能看内嵌编号
packet；硬限制 12 regions、240 lines/region、1,200 lines、32 KiB、3 candidates 和 wall timeout，使用
JSON-only + `--output-last-message`，禁止 `--output-schema`，不硬编码 model。实测 47 KiB/9 regions 在
75s 被完整进程组终止；25 KiB/9 regions 约 45s 返回合法 JSON。child 曾提出三轮 fair-lock 同 TS
删除窗口，但 parent 核对 owner 后发现 TiDB 新 TS 尚未写入 client-go committer，故判负。这证明新
分工有效：child 负责反例形状，parent 负责 owner transfer 实锤。

下一条且仅一条合法事务方向：`ASYNC_SECONDARY_SET_COMPLETENESS`。从当前源码追踪 async prewrite
可能成功的完整 key set，核对 primary lock 的 secondaries 在过滤、分 batch、Region relocation、
fallback、dedup 后是否仍 complete；先做 P/Q/F + highest consumer，不得先上 testbed。禁止重放
shared-lock FK、pipelined exclusive-end、status-cache、primary relocation 和 PR-review finding。

**2026-07-13 txn mutable-value checkpoint：命中一个新的 moderate bug id2220003，并把 packet
closure 与恢复所有权方法各推进一层，但尚未找到新的 severe cross-layer bug。** 先完成既定的
`ASYNC_SECONDARY_SET_COMPLETENESS`：client-go 对 accepted mutations 构造完整 secondaries，只在
primary request 携带全集，Region relocation 按 key 重建 primary；TiKV 将全集保存在 primary lock，
遇到非 async secondary 会强制走同步恢复。当前源码下没有找到“async success 但 accepted key 不在
recovery set”的路径。这里的重要修正是：child 返回 0 candidate 只对 packet 内闭包有效，parent 必须
再补 predecessor producer、filter/representation、lifetime/reset 和 highest consumer，才能接纳负结论。

随后从 savepoint restore differential 发现 `id2220003`：`TransactionContext.TemporaryTables` 作为
容器放在 `TxnCtxNoNeedToRestore` 本身可以成立，但 map value 内的 transaction dirty-size 是 mutable、
非单调且属于 savepoint window。设置 1 MiB 限制，在 savepoint 后写入两个 600000-byte 行再 rollback，
`COUNT(*)=0`，但下一条 1-byte INSERT 报 `ERROR 1114 table full`。只 snapshot/restore per-table size 的
本地反事实使同一矩阵 GREEN；testbed 8220955 在 exact TiDB `5c9198e9484d` 上 SQL-only RED。公开 issue
三组 post-RED 去重无 exact root，远端 `found_bug id2220003` 已入库；当前 113 surfaces、90 roots、
36 high、98 confirmed。质量必须诚实：这是自然可达、强 oracle、精确反事实、exact-commit live RED，
所以发现质量高；但最高后果只是事务内合法写入被拒绝，没有 durable wrong data、跨会话 corruption
或 limit bypass，因此 severity=moderate，不计入 severe 命中。

方法新增 S51 `SAVEPOINT_MUTABLE_VALUE_OWNER_CLOSURE`：对 normal/savepoint graph `N`、restore graph
`R`、post-restore consumers `C`，计算 `(mutable(N) intersect C) minus R`；对 map/slice/interface/
pointer/cache/handle 必须把 container membership 与 value lifecycle 分开审计。资产入口：
`assets/store/txn-savepoint-mutable-owner-results.jsonl`、
`docs/method-cases/ai-native-id2220003-savepoint-mutable-value-method-case.md`、
`docs/bug-drafts/ai-native-savepoint-local-temp-size-stale-draft.md` 及两份 RED/GREEN/live 日志。TiDB
临时验证改动已全部归位，pinned worktree clean；可复用 pack 已含 selector/obligation/oracle/natural
fault/scenario/schedule 和三次验证运行，`open_gaps=[]`。下一轮继续追 severe：优先找同一 mutable-owner debt
中能到达 key/predicate/row image/commit outcome/terminal truth 的 consumer，不围绕临时表大小做变体。

**2026-07-14 MDL-on txn severe hit：`id2340003` 证明 pipelined DML 会在 primary Commit 已持久化后返回普通失败。**
本轮不使用 PR/review/issue 选题，从 S48 current-source owner graph 比较普通 2PC 与 pipelined 专用终结路径：
`actionCommit` 在 primary RPC 响应丢失时设置 `undeterminedErr`，普通 `commitTxn` 会提升成
`ErrResultUndetermined`，但 `commitFlushedMutations` 直接返回 raw error。精确注入只丢第一条成功
`CommitResponse`，随后令 Commit 通道不可用；current upstream client-go `01bd8f9` + current nightly
TiKV `7ecce12` 上，fresh txn 读到 committed value，而原 Commit 返回普通
`injected commit transport outage`。只补 side-state promotion 后，同一 real-TiKV durable truth 不变，
错误变成 undetermined，RED/GREEN 成对闭合。testbed 8220955 只读确认 global/session MDL 都为 ON，
默认 DML type 为 STANDARD；目标只需 session 显式选择 BULK，未关闭 MDL。post-RED GitHub 与远端 bug
库无 exact root。远端 `found_bug id2340003/high/confirmed` 入库后为 117 surfaces、94 roots、40 high、
102 confirmed；后果记为 critical data integrity，但频率诚实限制在 opt-in BULK 加 after-apply response
loss。上游 issue 为 [TiDB #69821](https://github.com/pingcap/tidb/issues/69821)，带
`severity/critical`、`component/tikv-client`、`sig/transaction` 和 `found-by-ai`。新 selector
`SIDE_STATE_SEMANTIC_PROMOTION_BYPASS`：下层写 side state、canonical finalizer 提升、
specialized path 绕过、另一安全消费者仍信任 side state、public error class 控制 retry/connection/cleanup。
注入改进：按成功响应的语义高度选点，不能按“第一包”序号选点。资产入口：
`assets/store/txn-pipelined-undetermined-promotion-results.jsonl`、
`docs/bug-drafts/ai-native-pipelined-commit-undetermined-promotion-draft.md`、
`docs/method-cases/ai-native-id2340003-pipelined-undetermined-promotion-method-case.md`、
`scaffolds/client-go-tests/pipelined_undetermined_probe_test.go`。暂停门：不枚举 DML/error/timeout/key-count
变体；下一轮迁移 selector 到另一个 specialized terminal owner。

**2026-07-14 txn critical hit：`id2550003` 证明 recovery certificate 只覆盖 lock-bearing keys
会漏掉失败的 proof-only mutation。** 之前 `ASYNC_SECONDARY_SET_COMPLETENESS` 只问 accepted async
prewrite key 是否都在 secondaries，因此把“不写 lock”的 `CheckNotExists` 当成天然可排除成员。新一轮把
集合改成“所有逻辑提交前提减去 durable recovery evidence”，立即发现 current client-go 允许含
`CheckNotExists` 的事务走 async commit，却在 `asyncSecondaries` 中排除它。跨 Region 时 business primary
可先 prewrite 成功，proof batch 随后真实返回 `AlreadyExist`；若错误后的后台 cleanup 因 TiDB exit/OOM/
rolling restart 或 TiKV 路径长期失败而没完成，后续读在 TTL 后会用空 certificate 恢复 primary 为 commit。

真实 TiDB SQL 在 MDL 默认 ON、lazy uniqueness 默认 OFF-in-place、async 显式 ON、1PC OFF 下复现：事务
先插入已有 email 的 tentative row，再删除自己的 insert，同时将账户 `0 -> -100`；`COMMIT` 返回明确
duplicate，但 fresh session 触发 `ResolveLock action=commit` 后读到 `-100`。移除 200 ms Region delay 后
连续 3/3 RED。仅加入 `hasNoNeedCommitKeys => checkAsyncCommit=false`，相同 SQL/Region/fault 变 GREEN，
余额保持 0。远端 `found_bug id2550003` 已入库并更新为 `issue-filed`，上游 issue 为
[TiDB #69832](https://github.com/pingcap/tidb/issues/69832)，带 `found-by-ai` / `severity/critical`；
当前为 124 surfaces、101 roots、47 high、109 confirmed；post-RED
TiDB/client-go/TiKV issue 搜索无 exact root，#65757 是不同的 stale-secondary/minCommitTS 问题。新增 S55
`RECOVERY_CERTIFICATE_PROOF_CLOSURE` 与 O60；资产入口：
`assets/store/txn-async-checknotexists-proof-closure-results.jsonl`、
`docs/bug-drafts/ai-native-async-checknotexists-recovery-partial-commit-draft.md`、
`docs/method-cases/ai-native-id2550003-recovery-certificate-proof-closure.md`。临时源码改动已归位。

**2026-07-14 MDL-on txn/DDL cross-layer critical consequence hit：`id2700003` 证明 server-info
Restart 会在 membership publication 成功前安装 live replacement session，单次 publication 失败可永久
压掉 retry，并让 ADD INDEX 与旧事务共同造成 row/index 物理不一致。** 选题来自当前源码证明义务：
MDL DDL 的 wait set 来自 `/tidb/server/info`，而 MDL 事务在 commit 时令
`needCheckSchemaByDelta=false`，所以 membership publication 是两条安全链的共同 owner。独立 RED 前未用
PR review、issue 或修复历史。

探索过程保留了三个重要反例。直接 `SetSessionManager(nil)` 虽得到 COMMIT success + 1/0 + 8223，但只
证明后果，不证明生产可达；真实双 TiDB 进程整体 `SIGSTOP` 95 秒后，schema syncer 同时重启，validator
以 `restartSchemaVer` 拒绝旧事务并返回 8028，所以 broad node stall 是 GREEN；最初自定义
`failpoint.Inject` 没有执行 `make failpoint-enable`，marker 实际是 compile-time no-op，相关输出全部记
INVALID。最终 real-TiKV 精确调度只关闭 server-info session，保持 schema-sync live，在 Restart 前 pause，
让 replacement lease 创建成功但第一次 `StoreServerInfo` 明确返回
`mock store server info error`。当前源码随后不再重试，ADD INDEX 成功、旧 COMMIT 成功、table/index=1/0，
`ADMIN CHECK` 8223。仅在失败时 restore completed old session 并 close unpublished replacement，日志出现
第二次 Restart success，DDL 等旧事务，最终 1/1 + ADMIN green。

远端 `found_bug id2700003/high/confirmed` 已入库：129 surfaces、106 roots、52 high、114 confirmed；
post-RED GitHub 搜索无 exact root，尚未提 upstream issue。生产触发卡必须原样保留窄条件：server-info
lease 单独结束、schema-sync lease 继续存活；replacement grant 成功后五次 Put 失败，但 replacement
session 随后保持 live 并覆盖 DDL+COMMIT。MDL、事务和 90 秒 TTL 都是默认值，但故障顺序频率不能夸大。
新增 S37 extension `FAILED_PUBLICATION_LIVE_OWNER_RETRY_SUPPRESSION` 与 O65；方法硬门新增
`fault_activation_witness`，任何注入行必须在同一 run 证明目标 producer 真被执行。资产入口：
`assets/bug-db/ai-native-mdl-server-info-restart-publication.sql`、
`assets/store/logs/mdl-server-info-restart-publication-red-green.json`、
`docs/bug-drafts/ai-native-mdl-server-info-restart-publication-draft.md`、
`docs/method-cases/ai-native-id2700003-failed-publication-live-owner.md`、
`scaffolds/tidb-tests/ai_native_mdl_server_info_restart_test.go` 及两份 failpoint/GREEN patch。暂停本 root 的
DDL/事务/error/TTL 变体；下一轮迁移 S37 extension 到另一个 membership/routing/recovery consumer。

**2026-07-17 DefaultNotFound 历史复现之后的相邻 master 命中：`id2760003/moderate`。** 历史栈已用
单 TiDB、三 TiKV、MDL ON 复现精确 `KV:Storage:DefaultNotFound`；testbed build
`d573e284da773c820c1c313105b73d587378381b` 在 active statement owner 上已由 #61325/#61329 修复，
因此历史问题本身不是新 bug。增量方法没有重扫 GC，而是把同一个 live-snapshot 证明义务沿 owner
生命周期展开：`active statement -> detached lazy cursor`。当前 `TryDetach` 只把 `TxnCtx.StartTS` 写入
cursor，而 autocommit stale read 的真实身份在 `TxnCtx.StaleReadTs`，所以 owner UT 直接得到
`read_ts!=0/cursor_start_ts=0`，后续 `ReportMinStartTS` 越过 live TS。

真实协议 lift 在 testbed 8196300 上用 one TiDB/three TiKV、64 Regions、默认 DistSQL scan concurrency、
无额外 SQL、无 compaction：cursor 打开后 processlist 自然为 Sleep/TxnStart 空；TiDB 上报
`467725284651040769`，高于 live snapshot `467725273248038917`；PD GCV2 只推进到这个 TiDB 自己允许的
上界，第一次正常 `COM_STMT_FETCH` 返回精确 9006。只在 cursor handoff 回退到 `StaleReadTs/SnapshotTS`
后，新 UT 与已有 ordinary cursor UT 同时 GREEN。post-RED GitHub/远端库无 exact root，远端 bug 库
记录为 id2760003。质量边界必须保留：lazy cursor 默认 OFF，后果是明确 query failure，不是 silent
corruption，所以不算 severe 命中。新 selector 是 `LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF`：每个
registry bug 都要保存 `semantic identity -> owner A -> handoff -> owner B -> collector`，历史修复只让一个
owner GREEN 时，优先对 owner phase 做增量变异；PD blocker/合法上界本身是强 oracle，绕过 blocker 的
forced GC 不能算产品 RED。入口：`docs/bug-drafts/ai-native-lazy-cursor-stale-read-gc-draft.md`、
`docs/method-cases/ai-native-lazy-cursor-stale-read-gc-method-case.md`、
`assets/store/txn-lazy-cursor-stale-read-gc-results.jsonl`、
`scaffolds/top-level/ai_native_stale_cursor_gc_probe.go`。2026-07-17 再通过 GitHub API 核对当前 master
`94b834d94b604b1940ecc2c3064168337863269d`：`TryDetach` 与 `ReportMinStartTS` 的同一 source root 仍在，
所以这是 current-master bug；runtime RED 的 commit 与当前源码确认的 commit 必须分开记录。

**2026-07-17 current-master GC/txn 严重影响面扩展：没有新增 root，但 `id2580003` 已从
OFF-only UPDATE resurrection 扩到 FAST/STRICT INSERT 与 STRICT FK orphan。** 当前源码 pin 为 TiDB
`94b834d94b604b1940ecc2c3064168337863269d`、client-go `01bd8f99f4da`，真实 TiKV nightly
`c27c66202dcd2ec0113619c613e0dac3d17780b6`。MDL、FK 和 `foreign_key_checks` 运行时均为 ON，目标逐
session 关闭 1PC/async，使用普通 optimistic 2PC。小矩阵先证明已有行 UPDATE/UPDATE IGNORE/ON DUP/
REPLACE 在 FAST 下都被 `Assertion=Exist` 拦住；随后把证明表示改为 absent-row lazy INSERT：另一事务在
旧 startTS 后 INSERT->DELETE，同构无 GC 控制报 9007 且空表，GC+write/default CF compaction 后 FAST 与
STRICT 两格都 `COMMIT nil + fresh [[1 11]]`。更强 FK 格中，旧事务先验证 parent 并缓冲 child，另一事务
删除 parent；无 GC 控制报 9007/no orphan，GC 后 STRICT 仍 `COMMIT nil`，fresh anti-join 为 `[[1 1]]`。

根因/修复所有权仍与 #69833 相同：`KVTxn.Commit` 在 startTS 退休后进入 prewrite，没有 fail-closed
visibility admission；`AssertExist` 只是 UPDATE 形状的偶然 mask，lazy INSERT 的 `AssertUnknown` 和 FK
parent lock-only proof 仍依赖已被 GC 删除的 write history。因此不新增 bug 编号和 root 计数，但后果已
升级为默认 FK 开关下可持久化 orphan child，且 NextGen 默认 STRICT 也不是 closure。方法改进：mask
矩阵必须新增“证明表示”维度，按 existing/absent、row/index、lock-only/FK/proof-only mutation 变异，不能
在第一格 GREEN 后按 DML 语法扩散或关闭 selector。生产频率仍须诚实：需要 startTS 真正越过 GC 保护；
`tidb_gc_max_wait_time=600` 时是可压缩的 20-30 分钟链，24h 源码默认则接近 client-go 最大事务年龄。
资产入口：`assets/store/txn-gc-retired-start-ts-commit-results.jsonl`、
`docs/method-cases/ai-native-id2580003-safe-point-retirement-consumer-closure.md`、
`scaffolds/tidb-tests/ai_native_gc_expired_txn_assertion_fk_probe_test.go`。

**2026-07-25 跨模块 severe 命中：`id2820003` 证明批量操作使用冻结引用图会把 FK 静默绑定到错误
父表。** 本轮按用户要求离开事务主线，从当前 TiDB master `231dad5225f0` 的 collection-level proof
obligation 出发。`onRenameTables` 在循环外取得 InfoSchema，循环内逐项改名并共享
`foreignKeyHelper`；前一项可以创造新的中间名称和 FK 边，后一项仍只查语句开始前的 referred-FK map。
最小四步排列为 `p1 -> tmp, p2 -> p1, tmp -> p2, p3 -> tmp`：原始父表 `p1` 最终是 `p2`，实际 FK
却保留为 `tmp`，并因此指向原始 `p3`。

当前 master 定向 UT 与 one-TiDB/one-real-TiKV 都为 RED，FK 和 MDL 均为 ON，无并发、failpoint、重试或
节点故障。DDL 成功后，已有合法 child 立即失去正确 parent，错误 parent 可以授权新 child，正确 parent
可以被删除，`ADMIN CHECK TABLE` 仍为 GREEN。普通单表 rename 控制为 GREEN。仅让后续成员同时查询
已加载的 evolving FK edge，并在 frozen lookup 为空时仍持久化 loaded table，同一测试转 GREEN，闭合
因果链；该补丁只作反事实验证，已从 TiDB 工作树删除。

远端 `found_bug id2820003/high/confirmed` 已入库，当前为 133 surfaces、110 roots、55 high、117
confirmed；post-RED GitHub issue/PR 搜索无 exact root，尚未提 issue。新增 S63
`COLLECTION_SNAPSHOT_MUTATION_GAP`：定位“循环外 snapshot/cache/name graph + 循环内 key/edge mutation
+ 后续成员复用生成 key + 持久化 consumer”，用三到四步 permutation 验证 object identity、旧数据和
双向 enforcement。入口：
`docs/bug-drafts/ai-native-rename-tables-fk-name-reuse-draft.md`、
`docs/method-cases/ai-native-id2820003-batch-frozen-reference-graph.md`、
`assets/store/ddl-rename-fk-name-reuse-results-20260725.jsonl`、
`scaffolds/tidb-tests/ai_native_rename_tables_fk_name_reuse.sql`。暂停本 root 的 table-name、cycle、
cross-schema、FK action 变体；下一轮将 S63 迁移到恢复、批量权限/规则、备份 manifest、任务发布等
非事务 owner，继续只接收可落到持久化数据或发布状态的 high/critical 后果。

**2026-07-25 跨模块 severe 命中：`id2850003` 证明 NextGen `IMPORT INTO` 会把整批数据写入已被
`TRUNCATE` 退休的 table generation，并仍报告成功。** 当前 TiDB master 为 `231dad5225f0`。源码先在
user keyspace 生成包含完整 `TableInfo/table ID` 的 plan 和 import job，再创建 SYSTEM-keyspace DXF task；
Classic 路径会设置 `TableModeImport`，NextGen 路径跳过。`OnPrepare`、CSE worker 和 `finishJob` 都继续
消费冻结 ID，没有把它绑定到执行时仍存活的 table generation。

最强真实环境调度不使用产品 failpoint：先停 CSE TiKV worker，提交 detached import，确认任务入队后执行
普通 `TRUNCATE TABLE t`，table ID 从 44 变为 46，并在新表写入 marker；恢复 worker 后，job
`finished/row-count=2`，当前 `t` 只有 marker，底层按旧 ID 44 扫到两条导入 record key，当前表
`ADMIN CHECK` 仍成功。无 DDL 控制为 GREEN。原先的 atomic rename/swap RED 只作为 identity consumer
校准，因为 rename 可能跟随对象语义；升级到 generation retirement 后才通过 C3 severe admission。

远端 `found_bug id2850003/high/confirmed` 已入库，当前为 134 surfaces、111 roots、56 high、118
confirmed；post-RED GitHub issue 和远端库搜索无 exact root，尚未提 issue。新增 S64
`LIVE_RESOURCE_GENERATION_ACROSS_ASYNC_OWNER_HANDOFF`，并完善原 S handoff 思路：遇到 stale identity
后按 rename、replacement、retirement、reuse 分级；rename 用于定位，严重性必须由 replacement 或
retirement 加 live/retired 双归属 oracle 证明。入口：
`docs/bug-drafts/ai-native-import-into-truncated-target-generation-draft.md`、
`docs/method-cases/ai-native-id2850003-live-generation-handoff.md`、
`assets/store/import-into-stale-target-generation-results-20260725.jsonl`、
`scaffolds/tidb-tests/ai_native_import_into_truncated_generation_test.go`。暂停本 root 的 DDL verb、格式和
延迟枚举；下一轮把 S64 横向迁移到 TTL、BR/restore、distributed DDL 和其他异步 owner。

**2026-07-25 跨模块 critical-consequence 命中：`id2880003` 证明普通 BR snapshot restore 会与正常
应用 DML 形成持久化唯一索引损坏。** S64 横向迁移到 BR 后，最初用 `TRUNCATE` 证明 BR 冻结 rewrite
rule：官方 nightly BR 在旧 table ID 246 下恢复 128000 条记录并报告成功，当前 ID 258 只有 marker。
继续提升 consumer 后发现更强调度不需要 DDL：BR 创建目标后其 `TIDB_TABLE_MODE=Normal`，应用先写
`(id=1,u=900000000)`，随后 BR 按备份旧时间戳灌入原 row/index。

最强 RED 使用官方未修改 BR `a942e4684f`、TiDB nightly `ed2376acc6`、one TiDB/PD/real TiKV、MDL ON、
默认 checksum OFF 和普通 `--ratelimit 1`；没有 source patch、failpoint、进程暂停、节点故障或并发
DDL。BR 报 `Table Restore success/256000 KV`，但 primary 为 `128000/8192064000`，unique index 为
`128001/8192064001`；按旧键 `u=100001` 查询返回 `u=900000000` 且自谓词为 false，`ADMIN CHECK`
返回 8223，raw key 为 `128000 record/256001 table`。同备份同参数无 DML 控制为 GREEN。

远端 `found_bug id2880003/high/confirmed` 已入库：135 surfaces、112 roots、57 high、119 confirmed；
post-RED GitHub issue/PR 和远端库搜索无 exact root，尚未提 issue。新增 S65
`BACKDATED_PHYSICAL_INGEST_WITHOUT_WRITE_FENCE`：异步 identity 扫描命中后，不只测试
rename/replacement/retirement，还要测试同 generation 的 live mutation；若 physical consumer 写历史
时间戳，oracle 必须拆成 record/每个 index、强制访问路径和 predicate self-check。入口：
`docs/bug-drafts/ai-native-br-snapshot-concurrent-dml-index-corruption-draft.md`、
`docs/method-cases/ai-native-id2880003-backdated-ingest-write-fence.md`、
`assets/store/br-snapshot-concurrent-dml-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_br_concurrent_dml_repro.sh`。下一轮暂停 BR 的 key/index/DML/rate-limit
变体，把 S65 迁移到 Lightning、repair、index rebuild、log restore 或其他历史物理写入 owner。

**2026-07-25 S65 repair 负例已闭环:** `ADMIN RECOVER INDEX` 的旧快照候选没有命中数据损坏。
确定性探针在修复完成 table scan 与 missing-key check 后插入普通 UPDATE。默认
`tidb_txn_assertion_level` 下，UPDATE 因旧索引键已缺失而报 8141 `assertion: Exist`；显式关闭
assertion 后，UPDATE 可以提交，但修复事务在同一个旧索引键上报 9007 write conflict，
`RunInNewTxn` 整批重扫，最终 primary 为 `(1,20)`、强制 `idx_u WHERE u=10` 为空且
`ADMIN CHECK TABLE` 通过。临时 hook/test 已移除，TiDB worktree clean。方法改进：S65 在进入
真实环境矩阵前先检查 DML assertion、冲突键是否覆盖每个 stale owner、retry 是否重读来源。
资产见 `docs/method-cases/ai-native-admin-recover-index-concurrent-update-negative.md` 与
`assets/store/admin-recover-index-concurrent-update-negative-20260725.jsonl`。

**2026-07-25 跨模块 current-master 回归命中：`id2910003` 证明跨库 `RENAME TABLE` 后冷 TiDB 会把
`AUTO_ID_CACHE=1` 的生成 ID 倒退，并可通过 `REPLACE` 静默覆盖旧行。** 选题来自当前源码中的 owner
凭证不对称：`checkAndRenameTables` 明确把原始 schema ID 写入 `TableInfo.AutoIDSchemaID`，但
`NewAllocatorsFromTblInfo`、增量 InfoSchema、全量加载和 InfoSchema v2 都按当前 schema ID 重建
allocator。warm TiDB 的内存状态会遮住问题，cold peer/full reload 会从空的 local state 发布低水位。

官方未修改 nightly `ed2376acc6`、one PD/one real TiKV、MDL ON、无并发/failpoint/节点故障下，跨库
rename 前生成 ID 1/2，rename 后新启动 TiDB 显示 `AUTO_INCREMENT=0`：普通 INSERT 报 duplicate key 2；
非唯一索引表 INSERT 成功并留下两个 ID 2；最强格 `REPLACE` 返回
`affected_rows=2/LAST_INSERT_ID=2`，表从 `(1,order-1),(2,order-2)` 变为
`(1,order-1),(2,replacement)`，SQL 成功且 `ADMIN CHECK` 仍 GREEN。只让
`NewAllocatorsFromTblInfo` 读取非零 `AutoIDSchemaID` 的完整 server 反事实，在相同真实集群生成 ID 3、
affected_rows=1，并保留两条旧行；临时改动已归位。

post-RED 去重找到历史 [#55846](https://github.com/pingcap/tidb/issues/55846) 与修复
[#55847](https://github.com/pingcap/tidb/pull/55847)，因此记为 current-master regression，不宣称新 root
family；当前新增价值是 master 仍 RED，且 `REPLACE` 将历史 duplicate-key 后果提升为成功后的持久化数据
丢失。远端 `found_bug id2910003/high/known-regression/confirmed` 已入库：136 surfaces、113 roots、
58 high、120 confirmed，尚未新提 issue。新增 S66
`PERSISTED_OWNER_IDENTITY_CONSUMER_CLOSURE`：看到 old owner/generation/epoch/mapping 字段时，必须列全
writer/readers，并覆盖 warm writer、healthy cold peer、brand-new full load；命中 identity reuse 后继续
通过 REPLACE/upsert/ignore/cleanup 提升 consumer，不能停在第一条显式错误。入口：
`docs/bug-drafts/ai-native-autoid-cross-schema-rename-regression-draft.md`、
`docs/method-cases/ai-native-autoid-owner-reload-regression.md`、
`assets/store/autoid-cross-schema-rename-regression-results-20260725.jsonl`、
`scaffolds/tidb-tests/ai_native_autoid_cross_schema_rename_reload.sql`。暂停本 root 的 schema、ID 数量、
peer/restart 和 SQL 语法变体；下一轮把 S66 横向迁移到 placement、TTL、统计、恢复、任务和权限 owner。

**2026-07-25 跨模块 critical 数据损坏命中：`id2940003` 证明 Classic 和 NextGen 同表并发
`IMPORT INTO` 的 active-job precheck 不能保证单 owner，任务终止后仍会留下持久化表/索引损坏。**
选题来自 singleton-owner 证明义务：`GetActiveJobCnt` 先读 pending/running 数量，后续还会执行空表、
CDC/PiTR、对象存储检查，`CreateJob` 才在之后插入 pending job；两者之间没有 target-unique claim。
NextGen user-keyspace 路径跳过 `TableModeImport`。Classic 虽然设置该模式，但 mode 不携带 job owner，
`CanTransitionTo` 又允许 `Import -> Import`；两个 session 在 mode 发布前并发完成 plan 时仍可双双入场。

最小目标表 `t(v VARCHAR(100), UNIQUE KEY uk(v))` 没有主键，两份输入 `a1/a2` 与 `b1/b2` 完全不重复。
两个普通 session 同时提交 detached import 后，都按同一空表状态生成 hidden handle 1/2，record 与 index
KV group 分开 ingest。最终两个 job 都以 `ErrChecksumMismatch` 失败，但表扫描只有一组 2 行，强制唯一
索引扫描保留两组 4 条，`ADMIN CHECK TABLE` 报 8223；不同 run 的 record winner 会反转，坏形态稳定。

最终证据使用 current master `231dad5225f0` 的未修改产品 packages、NextGen TiKV/CSE `ce46fc5067`、
真实 PD/TiKV、user/SYSTEM keyspace、MinIO、MDL ON，无 failpoint、source hook、进程暂停、节点故障或
网络/磁盘错误。自然并发连续 3 次首轮均 RED；test-only check-to-create barrier 两次 RED 只用于机制
定位；相同环境单 job 对照为 `finished + table/index 2/2 + ADMIN GREEN`。post-RED GitHub issue 与远端
bug 库无 exact root。

远端 `found_bug id2940003/high/confirmed` 入库后为 137 surfaces、114 roots、59 high、121 confirmed。
新增 S67 `ATOMIC_ADMISSION_BEFORE_IRREVERSIBLE_PARALLEL_OWNERS`：看到 count/existence precheck 用来
证明 exclusive owner 时，必须检查 claim 是否与 read 原子；若不原子，用互不重复的逻辑输入去碰撞
owner 自己生成的 ID/range/epoch，并把 terminal、主产物、每个 sibling artifact 和结构检查闭包。
入口：`docs/bug-drafts/ai-native-nextgen-concurrent-import-index-corruption-draft.md`、
`docs/method-cases/ai-native-id2940003-atomic-admission-owner-claim.md`、
`assets/store/nextgen-concurrent-import-admission-race-results-20260725.jsonl`、
`scaffolds/tidb-tests/ai_native_nextgen_concurrent_import_data_corruption_test.go`。暂停本 root 的文件格式、
行数、索引、并发数和 object store 变体；下一轮把 S67 横向迁移到 DDL job、repair、TTL、stats、
placement 和其他 singleton-owner admission。资产库已导入 7 个新增资产、8 条关系、4 次运行和 1 个
target；按 `dxf/importinto + S67` 生成的复用包包含 8 个资产，`open_gaps=[]`。

**2026-07-25 Classic 扩展验证：同一 root 已在默认配置复现，不能再把 table mode 当作充分保护。**
本地一 TiDB、一 PD、一真实 TiKV，MDL ON；两个普通 mysql client 同时向空 hidden-row-ID 表提交互不重复
的文件。100 万行/文件、默认写速时，job 创建只差 130 微秒，两者都进入 ingest；job 8 checksum 失败，
job 9 报 finished。终态 record/index 为 100 万/200 万，按失败方 key 查会返回另一方 row，
`ADMIN CHECK TABLE` 报 8223。默认 10 万行另一次也 RED；观察用 1MiB 限速矩阵同样 RED。

严格对照：第一个 job 已 running 后再提交第二个会报 8258；单 owner 最终 record/index 10 万/10 万且
ADMIN GREEN。另一次并发虽然两个 job 都被创建，但 DXF 恰好串行，第二个在 ingest 前因非空表失败，
最终仍 GREEN。因而证明链为：双 admission 只是必要条件，两个 owner 都跨过物理 ingest 才形成 C3。
远端 `found_bug id2940003` 应更新为 generalized root
`import-target-active-owner-check-then-create-race`，不新增 surface/root 计数。新增 Classic evidence log、
table-mode fault asset、默认 RED/串行 GREEN runs 和命令行 scaffold。standalone Lightning 也可产生同一
坏形态，但官方物理导入契约明确要求导入期间停写，因此只保留为机制校准，不计新 bug。

**2026-07-25 跨模块 critical 数据丢失命中：`id2970003` 证明 `AUTO_ID_CACHE=1` 的
`AUTO_INCREMENT -> AUTO_RANDOM` 转换会迁移错误的 allocator owner。** 选题来自同一函数附近的阶段
不一致：`checkNewAutoRandomBits` 在 `SepAutoInc()` 为真时读取 `IncrementID`，随后
`applyNewAutoRandomBits` 固定读取、rebase 并删除 `RowID`。前者拥有原来的 auto-increment 高水位，
后者可以是 0；转换完成后，AUTO_RANDOM 的 incremental part 从低位重新开始。

未修改 current master `231dad5225` 明确成为 DDL owner 后，64 条旧行加 24 次 generated `REPLACE`
产生 12 次主键复用，最终只剩 52 条旧行；打包 nightly `ed2376acc6` 同样 RED。可复用 64 次脚本再次
命中 34 次 `affected_rows=2`，旧行从 64 条降到 30 条。所有 SQL 都返回成功，fresh read 证明旧 payload
消失，`ADMIN CHECK TABLE` 仍为 GREEN。默认 auto-ID cache 对照保留全部旧行；只让 apply 阶段在
`SepAutoInc()` 时选择 `IncrementID` 的 current-source 反事实也保留全部旧行。MDL ON，无并发、
failpoint、暂停、节点故障、retry、restart 或特殊隔离；触发需要 `AUTO_ID_CACHE=1` 和显式开启受保护的
conversion session variable。

远端 `found_bug id2970003/high/confirmed` 已入库：138 surfaces、115 roots、60 high、122 confirmed。
新增 S68 `ALLOCATOR_TYPE_MIGRATION_OWNER_TRANSFER` 与 O67
`GENERATED_ID_DISJOINTNESS_PLUS_PREIMAGE_ROW_PRESERVATION`。后续扫描不限制事务模块：优先查资源的
type/representation/namespace/owner 迁移，逐阶段比较 validation、apply、cleanup、recovery、rollback
使用的 accessor；命中 identity alias 后提升到成功的 overwrite/delete/cleanup consumer，并 fresh-read
旧 preimage。多 TiDB 的 DDL RED/GREEN 必须记录实际 owner 地址和 binary revision；本轮第一次
counterfactual 因未修改的 4000 仍是 DDL owner 而被判无效，这条检查已加入 oracle。

本地资产图已导入 7 个新增资产、8 条关系、4 次运行和 1 个 validated target；按
`ddl/autoid + S68` 生成的复用包包含 7 个 execution-verified 资产、4 次历史运行，
`open_gaps=[]`。

入口：
`docs/bug-drafts/ai-native-autoid-cache1-autorandom-migration-draft.md`、
`docs/method-cases/ai-native-id2970003-allocator-owner-transfer.md`、
`assets/store/autoid-cache1-autorandom-migration-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_autoid_cache1_autorandom_migration.sh`。暂停 cache 值、shard bits、行数与
SQL 写法变体；下一轮把 S68 横向迁移到 BR/restore、import/checkpoint、sequence、statistics、
placement、cache generation 和后台任务 owner。

### 2026-07-25: id3000003 - batch DROP 的 future-sibling 证明失效

范围已从事务模块放开到所有高危模块。新命中来自 DDL/FK：`DROP TABLE` 在 admission 和 owner
复查时都把完整对象列表当作 ignore set，却逐表调用独立的 `doDDLJob2`。父表排在子表前时，父表
job 依赖的是“未来子表会被删除”；父表提交后、子表 job 开始前，普通并发
`RENAME TABLE c TO c_survivor` 可以让这个承诺失效。

未修改 current master `231dad5225` 和官方 nightly 均 RED：父表优先的 batch DROP 与并发 RENAME
都返回成功，`c_survivor` 保留 `REFERENCES p(id)`；父表缺失期间普通 INSERT 产生新孤儿，重建
同名父表后旧孤儿仍在，后续非法写入又会被 1452 拒绝，`ADMIN CHECK TABLE` 仍为 GREEN。只把
子表移到列表最前的 matched GREEN 中，子表先消失、RENAME 返回 1146、没有 survivor。

远端 `found_bug id3000003/high/confirmed` 已入库：139 surfaces、116 roots、61 high、123
confirmed。新增 S69 `FUTURE_SIBLING_EFFECT_AS_ADMISSION_PROOF` 与 O68
`FK_EDGE_CLOSURE_AFTER_BATCH_TERMINAL`。方法论增量：审计 batch API 时，要区分一次 admission
与是否原子提交；逐项标出它依赖的 sibling 是 past/current/future；对 future sibling，在第一个
不可逆边界后立即做 rename/cancel/rebind，再用 sibling-before-boundary 作为一维 GREEN。这个
selector 可横向用于 restore object list、import manifest、cleanup queue、multi-resource config
和后台 task group。

入口：
`docs/bug-drafts/ai-native-drop-table-fk-future-sibling-race-draft.md`、
`docs/method-cases/ai-native-id3000003-future-sibling-admission.md`、
`assets/store/drop-table-fk-future-sibling-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_drop_table_fk_future_sibling_race.sh`。

### 2026-07-25: id3030003 - PiTR 必需修复失败后仍返回成功

跨模块扫描转到 BR/PiTR。当前 master 为修复 #69485，在日志回放后逐表读取持久化 IncrementID 并
ForceRebase `AUTO_ID_CACHE=1` 的 autoid service；但单表错误被标为 `Best effort`，只记 warning，
公开 helper 固定返回 nil，BR 后续仍可打印 restore success。

无需修改产品代码的 RED/GREEN 已完成。测试先模拟 raw replay：service 保持低位，TiKV 中加入恢复行
`id=2/restored-two` 并把持久化高水位推到 1004000。开启仓库已有
`pkg/kv/mockCommitErrorInNewTxn=return("no_retry")` 后，最终 rebase 记录 `mock commit error` 但
helper 成功；真实 `REPLACE` 返回 `ROW_COUNT=2,LAST_INSERT_ID=2`，fresh read 只剩
`id=2/replacement`。只关闭该错误，rebase 到 1004000，同一 SQL 分配 1004001，恢复行保留。

生产入口包括最终元数据事务的瞬时 TiKV 错误，以及 autoid owner 切换返回非 RPC 形态的
`not leader`；两者都可能只影响一张表。后续没有 generated-ID closure check。远端
`found_bug id3030003/high/confirmed` 入库后为 140 surfaces、117 roots、62 high、124 confirmed。
它有静默持久化数据丢失 consumer，但触发还要求 PiTR、`AUTO_ID_CACHE=1`、修复期错误和破坏型
upsert，因此不拔高为 critical。

新增 S70 `SAFETY_REPAIR_ERROR_DOWNGRADED_TO_BEST_EFFORT` 和 O69：修复如果是某个安全不变量的
唯一 closure owner，它的错误处理就继承原事故的严重性；逐个枚举 repair error，追到 public
terminal，复用原 bug 的最高 consumer，再用 one-error RED / exact no-error GREEN 验证。入口：
`docs/bug-drafts/ai-native-pitr-autoid-rebase-fail-open-draft.md`、
`docs/method-cases/ai-native-id3030003-required-repair-fail-open.md`、
`assets/store/pitr-autoid-rebase-failopen-results-20260725.jsonl`、
`scaffolds/tidb-tests/ai_native_pitr_autoid_rebase_failopen_test.go`。资产图已导入 7 个资产、
8 条关系、RED/GREEN 各 1 次和 1 个 validated target；按 `br/pitr + S70` 生成的复用包
`open_gaps=[]`。

### 2026-07-25: NextGen IMPORT INTO + 普通 DML 的 RED 属于已知 root

S65 从 BR 迁移到 NextGen `IMPORT INTO` 后得到两次真实 CSE/TiKV RED 和一次 matched GREEN。
探针在 `ImportEngine` 前暂停，普通 SQL 提交 `(id=1,u=900)`，再让导入按更早 TS 写入
`(id=1,u=100)`。导入最终因 checksum mismatch 失败，但表扫描只见导入值，`u=900` 的唯一索引仍
存在并返回该行，`ADMIN CHECK TABLE` 报 8223；两次 MVCC 证据均证明 application commit TS 更晚。
无并发 DML 的同路径对照 job finished，索引与记录一致。

post-RED 去重命中开放的 `pingcap/tidb#69182`：它已经明确要求 NextGen 导入进入
`TableModeImport`，目的就是阻止长导入期间的用户写入和 DDL。该问题的症状和严重性此前没有被实锤，
但 root 与修复边界相同，因此记为 `DUPLICATE_KNOWN_ROOT / NOT_ADMITTED`，不分配新 ID、不增加
root 计数。资产保留执行证据，用于严重性校准和后续 fix validation。

方法增量：去重要比较 root/fix boundary，不只比较症状；checksum failure 只是事后探测器，必须继续
检查失败后的持久化 owner；定时 hook 必须有“实际进入”证据，直接 `go test` 未做 failpoint rewrite
时 marker-style hook 会静默失效。入口：
`docs/method-cases/ai-native-nextgen-import-write-fence-known-gap.md`、
`assets/store/nextgen-import-concurrent-dml-known-gap-results-20260725.jsonl`。资产图已导入完整的
module/obligation/oracle/scenario/schedule/fault 复用包和 RED/RED/GREEN 三次运行，`open_gaps=[]`。

### 2026-07-25: id3060003 - BR 部分恢复把“入选批次”误当成“依赖闭合”

按最新要求继续跨模块扫描。新命中来自 BR snapshot `restore table`：官方文档支持只恢复一张表，
`filterRestoreFiles` 也严格按用户表名过滤；但后续 `BRIECreateTables` 为解决批量建表顺序问题，会
无条件关闭内部 session 的 FK 检查。代码只证明 child 被选中，就相信它引用的 parent 也在批次中。

最小真实环境矩阵使用同一份父子表备份。普通 SQL 在 parent 缺失时创建 child 会报 1824；两次只恢复
child 的官方 BR 命令都报告 `Table Restore success` 且 checksum 成功，但 parent 不存在，备份中的
`c(1,1)` 已成为孤儿，`foreign_key_checks=ON` 下继续插入 `c(2,999)` 仍成功，
`ADMIN CHECK TABLE` 也无法发现。改为完整恢复同一数据库后，parent/child 都存在、孤儿数为 0，同一
非法写入报 1452。

环境为一 TiDB、一 PD、一真实 TiKV，MDL、`tidb_enable_foreign_key` 和 `foreign_key_checks` 都开启；
无 source patch、failpoint、并发、暂停、节点或网络/磁盘故障。验证 BR revision `a942e4684f` 与当前
master `05b396fb6636` 的相关恢复路径无 diff。post-RED 去重中，#65175 是缺 parent 后的 DML
fail-open，#65256 只记录了 PiTR log 阶段的未完成建议；两者都没有闭合 snapshot table selector，也
不能解释恢复时已经产生的孤儿行，因此记为新 root。

新增 S71 `FILTERED_BATCH_MUST_CLOSE_DEPENDENCIES`：扫描所有 partial restore/import/export/migration/
cleanup API 时，先从对象 metadata 建引用图，再比较 selector 与传递依赖闭包；只要下游以
“internal/batch”为由关闭校验，就用普通 DDL 拒绝、partial RED、closed-set GREEN 三格验证，并把
terminal/checksum 降为次级 oracle。入口：
`docs/bug-drafts/ai-native-br-selective-restore-fk-dependency-draft.md`、
`docs/method-cases/ai-native-id3060003-filtered-batch-dependency-closure.md`、
`assets/store/br-selective-restore-fk-dependency-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_br_selective_fk_restore_repro.sh`。脚手架端到端复跑通过并自行清理；
远端 `found_bug id3060003/high/confirmed` 已入库，当前为 141 surfaces、118 roots、63 high、125
confirmed。资产图已导入 7 个本轮资产、5 条关系、RED/RED/GREEN 三次运行和 1 个 validated
target；复用包合计 8 个相关资产，`open_gaps=[]`。

### 2026-07-25: id3090003 - BR 的 absence precheck 没有绑定后续创建出的对象

继续跨模块扫描时，资产队列只剩低严重性 terminal-close 候选，因此回到 source proof
obligation。BR 已有 `checkTableExistence` 防止恢复进现存表，但后续批量建表固定使用
`OnExistIgnore`，再按表名读取当前对象；进入物理 ingest 前只检查 `IsCommonHandle`，索引 ID 则仅按
索引名映射。代码检查了“刚才不存在”，系统因此相信“现在同名的一定是 BR 创建的兼容对象”。

最小矩阵备份 `t(id PK,a,b,UNIQUE uk(a))`。默认 checkpoint 初始化期间，普通 DDL 在 precheck 后创建
同名 `t(... UNIQUE uk(b))`。两次官方 BR 都退出 0、checksum 成功并报告 `Table Restore success`；
目标 schema 保留 `uk(b)`，但物理 index key 是备份列 a 的 10/20。强制点查 `b=10` 返回
`b=100/predicate=false`，`b=100` 查不到，`ADMIN CHECK` 报 8223；普通
`UPDATE ... WHERE b=10` 还成功修改了实际 `b=100` 的行。

matched controls 闭合了时间维度：同名不兼容表在 BR 开始前存在时，precheck 以
`ErrTablesAlreadyExisted` 在 0 ranges 前拒绝；没有并发 CREATE 时，BR 恢复原 `uk(a)`，
`ADMIN CHECK` 通过，同一 UPDATE 影响 0 行。环境为一 TiDB、一 PD、一真实 TiKV，MDL ON、默认
checkpoint；无 source patch、failpoint、进程暂停、节点、网络或磁盘故障。验证 BR revision
`a942e4684f` 与当前 master `05b396fb6636` 相关路径无 diff。

post-RED 去重命中 #35215/#42893/#55087 和修复 PR #55044，它们建立了“恢复进已有表不安全”并加入
当前 precheck，但只覆盖检查时已经存在；没有发现 check-to-create 并发替换或不兼容 schema 仍成功的
同根问题，因此记为新的 TOCTOU root。

新增 S72 `CHECK_CREATE_USE_IDENTITY_CLOSURE`：审计所有 check-then-create 时，必须找到 create 返回并
传给最高 consumer 的稳定 identity token；若使用 `IF NOT EXISTS`/ignore 后又按 name/path 重查，就在
gap 中放入不同 fingerprint 的对象，用 preexisting GREEN、gap RED、no-competitor GREEN 验证。入口：
`docs/bug-drafts/ai-native-br-target-create-toctou-schema-corruption-draft.md`、
`docs/method-cases/ai-native-id3090003-absence-proof-idempotent-create.md`、
`assets/store/br-target-create-toctou-schema-corruption-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_br_target_create_race_repro.sh`。脚手架已从空目标端到端复跑，
自动完成 RED、preexisting GREEN、no-competitor GREEN 并清理临时对象。远端
`found_bug id3090003/high/confirmed` 已入库，当前为 142 surfaces、119 roots、64 high、126
confirmed。资产图已导入 7 个新资产、5 条关系、4 次运行和 1 个 validated target；按
`br/snapshot-restore + S72` 生成的复用包包含 9 个相关资产，`open_gaps=[]`。

### 2026-07-25: id3120003 - TiDB/TiKV 的 duration cast 差分导致普通 DML 改错行

范围放开后开始验证跨层表达式差分。出发点来自 TiKV duration-to-int 代码里留下的语义一致性线索，
随后把输入压缩为 `.499999/.500000/.500001` 三点边界，并用同一谓词分别走 TiKV pushdown 与
TiDB root evaluator。

`TIME(6)=-00:00:00.500000` 命中 RED：TiKV 把
`CAST(dur AS SIGNED)` 算成 `-1`，TiDB 算成 `0`。普通查询因此返回 id=2，却在同一结果行显示
`cast_value=0,predicate_holds=0`。`UPDATE ... WHERE CAST(dur AS SIGNED)<0` 的
`cop[tikv] Selection` 持久化修改 ids 2,3；强制 root evaluator 的 matched control 只修改 id=3。
邻接值 `.499999` 与 `.500001` 都一致，证明是负数 exact-half tie 规则分叉。

官方 nightly 的一 TiDB/PD/真实 TiKV、默认 strict sql_mode、MDL ON 环境直接复现，无并发、retry、
failpoint、source patch 或节点/网络/磁盘故障。TiKV 当前 master `91ccfb2126` 的现有
`test_duration_as_int` 加入兼容性断言后也直接失败：actual `-1`、TiDB expected `0`；探针已经撤销。
post-RED 在 TiDB、TiKV issue 和远端 bug 库中未找到同根问题。

新增 S73 `PUSHDOWN_ROWSET_SEMANTIC_CLOSURE`。方法增量是把 source clue 变成跨 evaluator 的小矩阵：
先用 EXPLAIN 证明 owner 真正在 TiKV，再比较 exact row IDs，把 predicate 投影回返回行形成
self-contradiction，最后只把 row-set mismatch 提升到 UPDATE/DELETE 等持久化 consumer。warning、
格式或错误文本差异不进入高危队列。该 bug 有持久化错改后果，但触发要求负 TIME 精确半秒和显式
cast，因此记为 high，不拔高为 critical。

入口：
`docs/bug-drafts/ai-native-tikv-duration-cast-half-tie-wrong-dml-draft.md`、
`docs/method-cases/ai-native-id3120003-pushdown-rowset-semantic-closure.md`、
`assets/store/tikv-duration-cast-half-tie-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_tikv_duration_cast_wrong_dml_repro.sh`。脚手架已独立复跑并自动清理；
远端 `found_bug id3120003/high/confirmed` 已入库，当前为 143 surfaces、120 roots、65 high、
127 `confirmed=1`。资产图已导入 7 个资产、6 条关系、3 次运行和 1 个 validated target；
复用包 `open_gaps=[]`。

### 2026-07-25: id3150003 - TiKV JSON-to-CHAR 漏传返回长度，默认 strict DELETE 静默删错行

S73 的下一轮没有继续随机扩表达式，而是比较 TiDB evaluator 使用的语义输入与 TiKV RPN capture
列表。TiDB `builtinCastJSONAsStringSig` 会把 JSON 文本交给
`ProduceStrWithSpecifiedTp(..., b.tp, ...)`；TiKV `cast_json_as_bytes` 只 capture `ctx`，
`JsonRef ConvertTo<String>` 也明确留下“TiDB 还有 ProduceStrWithSpecifiedTp”这一 FIXME。

小矩阵固定 JSON 值，只改变 `CHAR(n)` 边界。`12/CHAR(4)` 与 `1234/CHAR(4)` 两边一致；
`1234.5/CHAR(4)` 命中 RED。下推查询返回 ids 1,3，root 查询只返回 id=3；id=1 在同一结果中显示
`cast_value=1234,predicate_holds=0`。默认 strict mode 下，下推 DELETE 成功删除 ids 1,3，只留
id=2；相同 root-owned DELETE 报 1406，三行 preimage 全部保留。

环境仍是一 TiDB/PD/真实 TiKV、MDL ON，无并发、retry、failpoint、source patch 或节点故障。TiKV
current master `91ccfb2126` 的 focused assertion 也失败，实际返回六字节 `1234.5`，不是
`CHAR(4)` 的 `1234`；探针已撤销。post-RED TiDB、TiKV issue 与远端 bug 库没有同根问题。

新增 S74 `REMOTE_EVALUATOR_CONTEXT_CLOSURE`：候选生成从“函数实现不同”提升到“语义参数集合不闭合”。
先枚举 local 使用的 return flen/scale、charset/collation、padding、sql_mode/error policy、timezone
等输入，再与 protobuf 字段和 remote `capture=[...]` 做集合差；每次只改变一个缺失维度，分别比较
value、warning/error、row set，最后提升到 strict DML。该 bug 有直接静默数据删除后果，但仍要求
显式 undersized JSON cast，因此保持 high，不标 critical。

入口：
`docs/bug-drafts/ai-native-tikv-json-char-flen-wrong-delete-draft.md`、
`docs/method-cases/ai-native-id3150003-remote-evaluator-context-closure.md`、
`assets/store/tikv-json-char-flen-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_tikv_json_char_wrong_delete_repro.sh`。脚手架独立复跑并自动清理；
远端 `found_bug id3150003/high/confirmed` 已入库，当前为 144 surfaces、121 roots、66 high、
128 `confirmed=1`。资产图已导入 7 个资产、6 条关系、3 次运行和 1 个 validated target；
复用包 `open_gaps=[]`。

### 2026-07-25: S73/S74 后续负证据

为避免上下文压缩后重复，四个未过高危门槛的方向已标记 retired：

- 合法 typed DATETIME/TIME 的 FSP、零点和上界矩阵 push/root 一致；之前的差异来自畸形 VARCHAR
  fallback，不作为高质量输入。
- 五种常见 utf8mb4 collation 对 sharp-s、土耳其 I、组合音标、全角字符、尾空格和 emoji 的
  equality row set 全部一致。
- JSON string 数字前缀在 SELECT 上确有 Decimal 差分，但默认 strict DELETE 会在 TiKV 报
  truncation error，持久化 consumer fail-closed；没有计入高危 bug。
- TiKV `index_lookup_executor::next_task` 的错误 TODO 不会丢 handles：
  `advance_orders_index(..., false)` 把受影响行加入 `left_rows`，交回 TiDB lookup。

入口：`assets/store/cross-evaluator-negative-controls-20260725.jsonl`。重开条件已经写入每个 target，
下一轮只接受新的合法输入、不同 context、未闭合 handle owner 或默认/common 持久化 consumer。

### 2026-07-25: id3180003 - TiKV 下推 `WEEK(date)` 丢失 `default_week_format`，普通 DELETE 删错日期

S74 没有继续随机枚举函数，而是复用 id30034 已确认的 hidden input：
TiDB `builtinWeekWithoutModeSig` 会读 `GetDefaultWeekFormatMode`。沿新的 representation boundary
检查后发现，TiKV `week_without_mode` 虽然 capture `EvalContext`，context schema 里没有该字段，函数
直接固定 `WeekMode(0)`。

强 oracle 使用恒等式 `WEEK(d)=WEEK(d,@@default_week_format)`，不需要人工计算周数。设置常见的
ISO-style `default_week_format=3` 后，语义恒假的下推谓词返回 ids
`1,2,3,6,7,9,10,11`；这些行回到 TiDB 后投影出的 implicit/explicit week 完全相等，
`predicate_holds=0`。`SLEEP(0)` root barrier 返回空集。

生产形状用 `DELETE ... WHERE WEEK(d)=52`。在 12 个年界日期上，显式 mode 3 与 root evaluator
只匹配 id=5；普通 cop[tikv] 路径匹配并删除 ids 1,5,6,9。默认 strict sql_mode、MDL ON，一
TiDB/PD/真实 TiKV，无 prepared statement、并发、retry、failpoint、source patch 或基础设施故障。
两个副本 `ADMIN CHECK` 都通过，说明后果是逻辑数据丢失，不是结构性 index corruption。

post-RED 远端库只有 id30034 的 plan-cache root；公开 #69650 也是该 cache 问题，#9669/#21510
属于旧的本地/session loading。没有发现 TiKV pushdown 固定 mode 0 的同根 issue。2026-07-25
current TiDB `05b396fb66` 与 TiKV `91ccfb2126` 仍保留 local getter/remote literal 的不对称源码。

S74 增量：generic `capture=[ctx]` 不能算 context closure，必须逐字段证明 local getter 的值进入
protobuf 参数或 remote context，并被函数消费。资产复用路径为
`hidden getter inventory -> pushable signature -> transport set difference -> algebraic sibling oracle
-> persistent DML`。这次只需要一个 12 行矩阵。触发需非零 `default_week_format`，因此 catalog
记 high，不拔高为 critical。

入口：
`docs/bug-drafts/ai-native-tikv-week-default-format-wrong-delete-draft.md`、
`docs/method-cases/ai-native-id3180003-hidden-session-input-pushdown-closure.md`、
`assets/store/tikv-week-default-format-results-20260725.jsonl`、
`scaffolds/top-level/ai_native_tikv_week_default_format_wrong_delete_repro.sh`。
脚手架已独立复跑并自动清理；远端 `found_bug id3180003/high/confirmed` 已入库，当前为 145
surfaces、122 roots、67 high、129 `confirmed=1`。资产图导入 6 个资产、4 条关系、RED/GREEN
两次运行和 1 个 validated target；总计 564 revisions，RED/GREEN 各 119。

### 2026-07-25: id3210003 - partial TIMESTAMP index 把 writer 时区写进持久化成员资格

用户明确要求范围不要限于事务模块后，先收尾 partial-index 持久化成员转换。源码初看
`MeetPartialCondition` 使用 process-global `indexConditionECtx`，似乎已经隔离 session context；
单 session 的 timezone 矩阵也全绿。把证明义务从“evaluator 读了什么”扩成“operand 进入 evaluator
前由谁表示”后，得到稳定 RED。

同一个 UTC 时刻 `2024-12-31 20:00:00`，在 `-08:00` session 写成
`2024-12-31 12:00:00`，在 `+08:00` session 写成 `2025-01-01 04:00:00`；两行使用相同 `k=7`，
表上定义 `UNIQUE INDEX uk(k) WHERE ts >= '2025-01-01 00:00:00'`。切回 `+08:00` 后，两行显示为
相同 TIMESTAMP、谓词都为真，但 `IGNORE INDEX` 返回 1,2，`USE INDEX(uk)` 只返回 2，唯一约束已经
逻辑失效，`ADMIN CHECK TABLE` 报 8223。普通
`DELETE ... WHERE ts >= '2025-01-01 00:00:00' AND k=7` 走 `uk` Point_Get，成功返回
`ROW_COUNT=1`，却留下仍满足 WHERE 的 id=1。相同时区 control 的第二次插入正常报 1062，
`ADMIN CHECK` 通过。

环境是一 TiDB/PD/真实 TiKV、默认 strict sql_mode、MDL ON、fast table check ON；无并发、retry、
failpoint、source patch 或基础设施故障。测试 nightly `ed2376acc6` 与 current master
`05b396fb66` 的相关 partial-index 文件无 diff。post-RED 的远端 bug 库和 GitHub issue 搜索没有
exact root。

新增 `PERSISTED_EVALUATOR_CONTEXT_CLOSURE`：持久化表达式审计必须同时枚举 evaluator context 和
operand representation context；固定 evaluator 不能证明输入已经 canonical。最小矩阵固定
`schema expression + canonical value`，只改变 writer representation，再用 source-of-truth row set、
derived structure、唯一约束和 DML closure 做 oracle。远端 `found_bug id3210003/high/confirmed`
已入库，当前为 146 surfaces、123 roots、68 high、130 confirmed。catalog 不标 critical，因为还要求
TIMESTAMP partial index 与混合 session 时区。

入口：
`docs/bug-drafts/ai-native-partial-index-timestamp-session-timezone-draft.md`、
`docs/method-cases/ai-native-id3210003-persisted-evaluator-context-closure.md`、
`assets/store/partial-index-timestamp-timezone-results-20260725.jsonl`、
`assets/store/logs/partial-index-timestamp-timezone-red-control-20260725.log`、
`scaffolds/top-level/ai_native_partial_index_timestamp_timezone_repro.sh`。脚手架已从空库独立复跑并
自动清理。下一轮把该 selector 横向迁移到 generated column、索引 backfill/check 和其他持久化
derived state，不枚举更多 offset、timestamp 或同根 DML。

### 2026-07-25: id3240003 - 等价组合绕过 unsafe expression-index 门并触发直接数据丢失

把 id3210003 的 persisted-context selector 迁移到 generated column 后，先得到 STORED generated value
与当前表达式不一致；继续提升到 VIRTUAL generated column + ordinary index 后，形成更硬的新 root。

TiDB 默认拒绝 `CREATE INDEX ... ((DATE(ts)))`，明确报 8200：unsafe function 需要
`allow-expression-index`。但等价写法
`d DATE AS (DATE(ts)) VIRTUAL, INDEX idx_d(d)` 被默认接受。源码中
`checkIllegalFn4Generated` 对两种语法都会记录 `hasNotGAFunc4ExprIdx`，却只在
`genType==typeIndex` 时拒绝；手写 generated column 作为 `typeColumn` 进入，后续普通索引不再复核
源表达式。

真实 TiKV 最小 RED 只需一行。`+08:00` 写入
`ts='2025-01-01 04:00:00'`，索引持久化 key `2025-01-01`；`-08:00` 读取时同一 stored TIMESTAMP
显示为 `2024-12-31 12:00:00`，virtual `d` 与 `DATE(ts)` 都是 `2024-12-31`。此时
`IGNORE INDEX` 对 `d='2025-01-01'` 返回空，默认/`USE INDEX` 返回 id=1，且同一行投影
`predicate_holds=0`。普通 DELETE 走 IndexRangeScan，成功删除 1 行；matched root DELETE 删除 0
并保留该行。默认 fast `ADMIN CHECK TABLE` 在这个 data-loss 方向仍通过。相同时区 control 和
DATETIME cross-timezone control 都为 GREEN。

环境为一 TiDB/PD/真实 TiKV、默认 strict sql_mode、MDL ON、默认禁用 unsafe expression index；
无 partial index、并发、retry、failpoint、source patch 或基础设施故障。相关 source 在测试 nightly
`ed2376acc6` 与 current master `05b396fb66` 间无 diff。post-RED GitHub 和远端库无 exact root；
与 id3210003 的 partial-index membership owner 不同。

新增 `COMPOSABLE_SAFETY_GATE_CLOSURE`：把被直接入口拒绝的对象正规化为 expression、derived state、
context 和 consumer，再枚举语义等价的组合入口；若组合跳过同一 admission predicate，就直接使用
原拒绝理由选择差分维度，并提升到最高不可逆 consumer。产品自己的 rejection 因此同时充当 hypothesis
和 negative control，比随机 fuzz generated expressions 更快。

远端 `found_bug id3240003/high/confirmed` 已入库，当前为 147 surfaces、124 roots、69 high、
131 confirmed。按现有 catalog 约定存 high，但这是默认配置、常见 schema pattern 下的直接静默数据
丢失，具备 upstream `severity/critical` 的定级依据。资产图为 577 revisions、RED 121、GREEN 122、
validated targets 65。

入口：
`docs/bug-drafts/ai-native-virtual-generated-timestamp-index-timezone-data-loss-draft.md`、
`docs/method-cases/ai-native-id3240003-composable-safety-gate-closure.md`、
`assets/store/virtual-generated-timestamp-index-timezone-results-20260725.jsonl`、
`assets/store/logs/virtual-generated-timestamp-index-timezone-red-control-20260725.log`、
`scaffolds/top-level/ai_native_virtual_generated_timestamp_index_timezone_repro.sh`。脚手架已从空库
独立复跑并自动清理；本 root 暂停枚举函数、offset、storage mode 和 DML 变体。

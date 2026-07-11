# GitHub DDL Held-Out 复盘: Review 基线与测试方法论改进
> 2026-07-09。目的: 用从 GitHub 拉取的历史 DDL bug 检验 AI-native 方法论的召回边界。重要口径修正:本文件里的 `49/82` 是 **PR review / 静态重发现** 基线,不是当前测试 harness 的召回率。后续要单独回答:测试方法论能不能把这些历史 bug 跑出来。

## 1. 数据源和判据:这是 PR review 基线,不是测试召回率

本次只使用本地已归档语料和 hitcheck 结果:

- 语料: `/Users/bba/pc/ai-bug-dataset/out/20260206_082028_ddl_validation_rootcause_closed_20240101_20250831_full/`
- 验证集: `dataset.validation.selected.csv`,共 82 个 DDL validation root-cause case,来自 2024-01-01 到 2025-08-31 期间的 closed bug/fix/intro PR 链路。
- 最强当前评测 run: `/Users/bba/pc/ai-bug-dataset/out/20260208_232200_re2_ddl_validation_selected_csv_gpt52_c20_ddldocs/report.md`

三个可比 run 的结果,回答的是“只看 intro PR 及 DDL 上下文,AI review 能不能提前指出同类风险”:

| run | 上下文 | FOUND | NOT_FOUND | UNCERTAIN | 结论 |
|---|---:|---:|---:|---:|---|
| `20260208_021423...c20` | generic RE2 | 42/82 | 37/82 | 3/82 | 只有通用 review/invariant 能力 |
| `20260210_121719...c40_noddl` | 更大预算,无 DDL docs | 44/82 | 35/82 | 3/82 | 预算增加只小幅提高 |
| `20260208_232200...c20_ddldocs` | DDL docs + DDL battery | 49/82 | 29/82 | 4/82 | DDL 方法论上下文有效,但不够 |

所以 PR-review 侧答案很直接: **当前 review 方法论不能把 GitHub DDL bug 都提前看出来**。它能把可从源码抽出明确证明义务的一大块 bug 重新发现出来;但对故障注入、外部系统、拓扑变化、压力/资源、commit 边界、错误上下文保真等 bug 类仍明显漏召。

一个有用的质量信号: hitcheck 自带难度评分里,FOUND case 的平均 `review_difficulty` 是 3.33,NOT_FOUND 是 7.52;FOUND 的平均 `design_detectability` 是 3.39,NOT_FOUND 是 5.86。这说明 PR-review 差距不是噪声,而是方法边界。

## 1.1 测试方法论要另起一轴

测试发现和 PR review 发现不是一回事:

```text
PR review 问题:
  看 intro PR/diff/源码,能不能推导出 P/Q/F 风险?

测试方法论问题:
  能不能把这个风险变成一个可执行矩阵,并用强 oracle 在 vulnerable revision 上跑红?
```

因此 held-out 评估要拆成两列:

| 维度 | 问什么 | 证据 |
|---|---|---|
| review detectability | 源码/PR 里能否提前看出证明义务 | blind review hitcheck |
| test detectability | 是否存在低噪声可执行 oracle | fail-on-intro/pass-after-fix test,或 failpoint/topology harness |

对当前测试方法论来说,更合理的问题不是“能不能在当前 master 上复现历史 bug”——很多 bug 已经修掉了;而是:

```text
如果切到 intro PR 对应的 vulnerable revision,
AI 能不能从 proof obligation 生成一个最小测试,
让它在 intro/fix 前后形成 red/green 差分?
```

已有 hitcheck 的 `test_detectability_1to10` 只能作为粗 proxy,而且 0 值看起来像未有效打分/不适用,不能直接当覆盖率。剔除 0 后,65 个带正分的 case 中:

- 低成本测试候选(1-3):30 个,其中 20 个在 PR review run 中 FOUND,9 个 NOT_FOUND,1 个 UNCERTAIN。
- 中等成本(4-5):18 个,其中 10 个 NOT_FOUND。
- 困难(6-8):17 个,其中 10 个 NOT_FOUND。

这说明有一批 PR review 没看出来的 bug,测试方法论反而可能更容易发现,因为它们有明确行为 oracle 或负例输入;也有一批必须靠故障注入/拓扑/压力 harness,不是普通 SQL 小矩阵能解决。

## 2. PR review 方法为什么能 work

PR-review FOUND 的主因不是“模型运气好”,而是 DDL 上下文把搜索空间压窄了。

最有效的形态有四类:

1. **对象/引用 ownership**
   DDL 改对象后,引用必须 rewrite、block 或 cleanup。这个模型能覆盖 rename/drop/truncate/exchange/recover 等路径上的 side metadata、FK、policy、stats、index、hidden object 问题。

2. **生成 artifact 的 owner/cardinality**
   例子: `fix48594/issue48304` 的 MVI empty-array case。源码里 `GenIndexKVIter` 对空数组的 cardinality 义务很明确:空数组应产生 0 个 index KV,不是 1 个普通 KV。oracle 是 `ADMIN CHECK` 或 index/table rowset。

3. **状态机转移和 sibling path**
   DDL job、disttask、rollback/cancel/retry 都有显式状态转移。当前方法在“状态转移规则写在源码里,且可以静态检查”的 case 上有效。

4. **强 oracle 可直接闭环**
   `ADMIN CHECK`, `USE INDEX` vs `IGNORE INDEX`, current schema vs side metadata, management round trip, safe path vs fast path 这些 oracle 噪声低,所以小矩阵一旦标红就容易实锤。

抽象成一句话:

```text
源码/历史行为里存在一个清晰的 P/Q/F 证明义务
且可以压成小矩阵
且有强 oracle 验证红格
=> AI 能高效重现历史 bug,也能挖出新 bug
```

## 3. 当前方法漏在哪里

29 个 NOT_FOUND 和 4 个 UNCERTAIN 不是同一种失败。粗分后有几类高价值缺口。

### 3.1 故障注入/外部系统类

典型 miss:

- `issue47992/fix48185`: PD batch scan region under import/load。
- `issue48049/fix48173`: server-disk import compressed file。
- `issue48164/fix48163`: S3 concurrent uploader overwrites real error。
- `issue48680/fix48687`: PD member change 后 dist task add index 失败。
- `issue50451/fix59694`: external storage network delay 下 read range 超限。
- `issue51846/fix52315`: PD leader network partition 下 add index canceling。
- `issue61537/fix61502`: 多集群共享 `tidb_cloud_storage_uri` 覆盖文件。

这些 bug 的共同点是:源码中可以看到“调用外部系统”,但红格通常需要 PD/S3/MinIO/network/topology/fault timing 才出现。普通 SQL 矩阵和源码 review 不会自然覆盖。

方法缺口:

```text
缺少 ENV/FAULT selector:
  外部系统调用 + retry/cancel/close/error wrapping + one-shot stream/request body
  => 必须配套 fault-injection oracle,不能只靠静态 P/Q。
```

### 3.2 生命周期/commit 边界类

典型 miss:

- `issue59055/fix59157`: DDL notifier handler 成功后,内部 SQL COMMIT 失败,但内存 processed 状态已经推进,导致 delivery guarantee 失效。
- `issue53843/fix53849`: cancel add index 时 ingest writers leak。
- `issue49117/fix49764`: context canceled 被嵌在 `awserr` 内部,外层错误分类没识别。
- `issue60433/fix61289`: 错误持久化到系统表时丢失 outer context。

这些不是传统 DDL schema oracle,而是“副作用什么时候才算生效”的问题。

新增证明义务:

```text
Durability boundary:
  只有 durable commit 成功后,内存状态/processed flag/ack 才能前进。

Lifecycle exactly-once:
  open/register/start 之后,success/error/cancel/owner switch 每条路径都必须 close/unregister/finish 一次。

Error identity preservation:
  retry/持久化/wrap 后,root error identity 和用户可见 context 不能丢。
```

### 3.3 压力/性能/资源类

典型 miss:

- `issue58450/fix58615`: ingest writer memory OOM。
- `issue59495/fix59496`: prefetch reader performance not expected。
- `issue50297/fix50304`: test-only data race。

这些 case 可能是真问题,但不一定适合作为当前“高质量 correctness bug”主线。它们需要 counter oracle,profiling,stress 或 race detector,和 rowset/schema oracle 不是一套。

处理规则:

```text
STRESS_PERF / TEST_ONLY 单独分层:
  可作为 selector 灵感,但不计入当前 DDL correctness 方法论的主召回目标。
```

### 3.4 兼容性/错误消息/边缘语义类

典型 miss:

- `issue51324/fix51309`: MySQL 8.0.29 default-value compatibility。
- `issue51703/fix51704`: default expression 错误消息更合适。
- `issue55565/fix55566`: `SHOW TABLES` 返回 temporary table。
- `issue56930/fix56964`: charset conversion 需要重新检查 index key length。

这些需要明确 reference contract。没有 MySQL/reference oracle 时,AI 容易只看源码局部,不会主动枚举精确兼容性。

方法补强:

```text
Compatibility oracle must be explicit:
  如果 bug 依赖 MySQL/文档兼容性,先把 reference contract 拉进 card,
  否则不要把“没想到”算成 selector 失败。
```

## 4. 改进后的 DDL proof-obligation taxonomy

原来的 DDL 主线主要是:

```text
DDL 改对象后,所有引用必须 rewrite 或 block。
```

GitHub held-out 说明这只是 DDL 的一个子集。需要扩成 DDL pipeline proof obligations:

| 代码 | 义务 | 红格信号 | 主要 oracle |
|---|---|---|---|
| S-OBJ | 对象/引用 ownership | 改对象后旧引用仍 live,或新引用不受保护 | rewrite/block/cleanup round trip |
| S-ART | 生成 artifact owner/cardinality/type | flatten 后 owner/type bit 丢失,空集合变 1 条,多 owner ordinal 误判 | `ADMIN CHECK`, rowset, duplicate/liveness |
| S-STATE | job/task/subtask 状态机 | 非法转移,终态不一致,ack 早于 durable commit | state table, retry/delivery, liveness |
| S-LIFE | resource lifecycle | cancel/error/owner switch 后 leak/double-close/stuck | fault injection + leak/liveness/no-panic |
| S-ERR | error identity/context | root error 被覆盖,wrap 后分类丢失,持久化丢 outer context | injected error identity, error-chain check |
| S-RETRY | retry/input cursor idempotence | one-shot body/reader/request offset 在 retry 后错位 | retry injection, byte/count monotonicity |
| S-CFG | config/session/input propagation | 清了一个 stale input,漏了 sibling input | current-session reference, downstream effect |
| S-CACHE | schema/cache/snapshot freshness | cached schema/stats/session side row 与当前对象不一致 | current schema vs side metadata + behavior |
| S-ENV | external topology/environment | PD/S3/network/upgrade/cluster namespace 才触发 | topology/fault-injection harness |

关键变化:以后看到 DDL bug,先不要问“属于不属于 reference ownership”,而要先问它落在哪个 pipeline obligation。只有 S-OBJ 适合现有 object-reference 矩阵;S-LIFE/S-ENV 必须先建故障注入或拓扑 oracle。

## 5. 改进后的 held-out 判读规则

每个 GitHub bug 不再只判 FOUND/NOT_FOUND,而要补四个字段:

```text
discoverability:
  SQL_ONLY          当前 testbed/SQL 小矩阵即可复现
  SOURCE_ONLY       源码能提出证明义务,但没有便宜 SQL oracle
  FAULT_INJECTION   需要 failpoint 或错误注入
  CLUSTER_TOPOLOGY  需要 PD/TiKV/owner/upgrade/network 拓扑
  STRESS_PERF       需要压力、race、profiling 或资源计数
  LOW_VALUE         错误消息/test-only/纯性能,不作为当前 correctness 主线

obligation:
  S-OBJ/S-ART/S-STATE/S-LIFE/S-ERR/S-RETRY/S-CFG/S-CACHE/S-ENV

oracle_gap:
  是否已有强 oracle;没有的话,miss 是 oracle-mining ticket

selector_gap:
  是否已有 selector 能提前把这个 case 标红;没有的话,抽象成 selector 候选
```

这样 held-out 的意义会从“模型有没有猜中 issue”变成“方法论还缺哪类义务和 oracle”。

## 6. 从测试方法论看,哪些历史 bug 能被发现

当前测试方法论的强项不是“随机跑更多 DDL”,而是把 review 抽出的 P/Q/F 义务变成可执行 oracle。因此它能覆盖的历史 bug 大致分三层。

### 6.1 可以被当前测试方法直接覆盖

条件:

```text
SQL 或 testkit 可构造触发输入
+ 有强行为 oracle
+ 不依赖外部拓扑/压力/真实故障
```

典型类型:

- DDL 后 schema/side metadata/current object 不一致。
- index/table rowset 或 `ADMIN CHECK` 能直接发现的数据/索引不一致。
- MySQL/reference contract 可用的兼容性语义。
- error code/error message 有明确可断言 contract 的负路径。

这类 bug 即使 PR review 没提前指出,测试方法也可能发现。例子包括 temporary table 语义、FK rename/change metadata、charset conversion 后 index key length、default-value compatibility 等:关键不是 review 是否聪明,而是有没有把 contract 写成一个小矩阵。

### 6.2 需要补 failpoint/fault harness 后才能覆盖

条件:

```text
P/Q/F 可以写清楚
但红格必须在 commit failure / cancel / retry / close / wrapped error 里触发
```

典型类型:

- DDL notifier COMMIT 失败后 processed flag 不能前进。
- ingest writer cancel/error path 必须 exactly-once cleanup。
- S3 uploader/pipe 并发失败时 root error 不能被覆盖。
- context canceled 被多层 wrap 后仍要正确分类。

这些是测试方法论下一步最值得补的地方。PR review 难发现,但一旦有 failpoint,测试反而很硬:注入一个错误,断言状态不推进、资源不泄漏、错误 identity 保留。

### 6.3 当前测试方法不应声称覆盖

条件:

```text
需要 PD/TiKV/S3/MinIO/network/upgrade/multi-cluster/stress/profiling/race
```

典型类型:

- PD member/leader/network partition 下的 add index。
- import/load 下 PD batch scan region。
- external storage network delay 和多集群 URI namespace。
- ingest writer OOM、prefetch 性能、test-only data race。

这些不是“测试发现不了”,而是需要另一个测试层:拓扑/故障/压力 harness。把它们塞进普通 SQL 小矩阵会污染方法论。

### 6.4 2/3 类才是下一阶段 AI-native fuzz 的关键

前一阶段的成功主要来自“源码语义分析 -> SQL 小矩阵 -> 强 oracle”。这对 S-OBJ/S-ART/S-CACHE 很有效,因为输入空间主要是 SQL/DDL 语法和对象状态。

但 6.2/6.3 这两类的输入不是 SQL 本身,而是:

```text
fault schedule:    commit fail / RPC cancel / close error / retry N / error wrap
lifecycle schedule: open -> write -> flush -> cancel -> cleanup -> owner switch
topology schedule: PD leader change / member replace / TiKV unavailable / network delay
resource schedule: memory pressure / slow external storage / many tables / large regions
```

所以 AI-native fuzz 的生成对象要升级:

```text
旧 fuzz:
  生成 SQL/schema/data

新 fuzz:
  生成 SQL workload + fault schedule + topology/resource schedule + oracle probe
```

这也是为什么普通 PR review 和普通 SQL fuzz 都不够:

- PR review 能指出“这里可能有 cancel/retry/lifecycle 风险”,但很难枚举真实触发时序。
- 普通 SQL fuzz 能跑很多语法组合,但不会主动在 COMMIT 后、cleanup 前、RPC wrap 内部、owner handoff 中间打断系统。
- AI 的价值是读源码后找出**应该在哪里打断**:哪个 durable boundary,哪个 exactly-once cleanup,哪个 retry cursor,哪个 external call,哪个 owner handoff。

下一阶段的标准卡片应从 `P/Q/F/O` 扩成:

```text
Target:
  目标 DDL/worker/external call

Boundary:
  哪个状态边界不能提前推进? 哪个资源必须 exactly-once cleanup?

Fault point:
  在哪一行/哪个接口/哪个状态后注入 fail/cancel/delay?

Schedule matrix:
  success / fail-before-side-effect / fail-after-side-effect / cancel / retry / owner-switch

Oracle:
  state table, in-memory/durable agreement, no leak, no double-close, root error preserved,
  retry can make progress, user-visible DDL job terminal state合法

Control:
  no-fault, failpoint-not-hit, safe sibling path, or fixed revision
```

如果这套能跑起来,AI-native fuzz 的核心就不再是“AI 多生成几条 SQL”,而是“AI 从代码里自动挖出 fault injection 的最小时序矩阵”。这正好补上 GitHub held-out 里 PR review 和 SQL 小矩阵漏掉的部分。

### 6.5 LOOP v2 的出发点:先挖 boundary,再设计实验

这里不能从“怎么加 failpoint / 怎么写测试”开始。测试设计只是中间手段,真正的出发点仍然是旧 LOOP 的前半段:从源码和历史 bug 中找**系统相信了什么**、**哪个边界一旦被打断就会错**。

对于 6.2/6.3,前半段要从 SQL proof obligation 升级为 boundary mining:

```text
历史 bug / 最近 PR / 源码调用链
-> 找可疑 boundary
-> 写出系统在 boundary 后相信的 Q_claim
-> 找 side effect 是先发生还是后 durable
-> 找谁会在 retry/cancel/owner switch 后再次消费这个状态
-> 评估是否有用户可见 consequence
-> 再决定需要 SQL、小 failpoint、临时日志,还是拓扑 harness
```

高价值 boundary selector:

| selector | 源码信号 | 核心问题 |
|---|---|---|
| durable-before-ack | handler 成功后再 COMMIT,或内存 flag 先推进 | COMMIT 失败后系统是否仍认为已处理? |
| register-cleanup pair | `Register/Open/Start` 与 `Close/Clean/Unregister` 分散在多路径 | cancel/error/owner switch 是否 exactly-once? |
| retry cursor | reader/body/request/channel 只能消费一次,外层有 retry | retry 后输入是否被重置或重建? |
| wrapped error | 外部 SDK/RPC 错误多层 wrap,上层按类型分类 | root error identity 是否保留? |
| state split brain | 同一状态同时存在 memory + system table + external file | 哪个状态是事实源?失败后是否一致? |
| owner handoff | 长 DDL 中 owner/PD leader/session/cluster namespace 可变化 | handoff 后旧 owner 的中间状态谁接管? |
| external namespace | S3 path/temp dir/URI 由用户配置或集群共享 | 不同集群/任务是否会互相覆盖? |
| pressure gate | memory/quota/backpressure/channel buffer 决定是否推进 | 压力下是限速、失败、还是留下半成品? |

这一步的产物不是测试,而是一张 boundary card:

```text
Boundary:
  哪个 commit/cancel/retry/cleanup/handoff 边界?

Q_claim:
  过了这个边界后,系统相信什么已经成立?

Side effects:
  memory / system table / external storage / worker goroutine / lock / file / error chain

Consumers:
  retry path / cleanup path / next owner / user-visible query / background scheduler

Consequence:
  data loss / wrong success / stuck job / leak / duplicate work / unreadable error / GC block

Experiment need:
  SQL-only? failpoint? temporary hook/log? topology schedule? stress counter?
```

只有 boundary card 过关,才进入“怎么改 TiDB、怎么设计测试”。这样可以防止把 AI-native fuzz 退化成到处撒 failpoint。也就是说,LOOP v2 的前半段仍然是分析,后半段才是实验系统。

### 6.6 LOOP v2:AI 不只生成测试,还要搭实验系统

这一步比之前的 LOOP 要求高很多。旧 LOOP 的主动作是:

```text
读源码/历史 bug
-> 抽 P/Q/F
-> 写 SQL 小矩阵
-> 用现有 oracle 验证
-> 命中后反推 selector
```

这对 SQL_ONLY/SOURCE_ONLY 很强,但对 6.2/6.3 不够。新 LOOP 要允许 AI 临时改 TiDB,因为很多红格没有现成入口:

```text
读源码/历史 bug
-> 抽 lifecycle/fault/topology boundary
-> 自动选择或新增注入点
-> 临时修改 TiDB: failpoint / hook / trace log / assertion / debug counter
-> 构造 schedule matrix
-> 跑 workload + fault schedule
-> 收集 event trace + 强 oracle
-> red/green 后回滚实验补丁,只沉淀 selector/oracle/harness
```

这里的“改 TiDB”不是为了修 bug,而是为了制造可观测、可控的实验条件。原则:

- **实验补丁必须短命**:只用于注入/验证/加日志,不和产品修复混在一起。
- **注入点来自源码义务**:不能到处撒 failpoint;必须能说清楚它对应哪个 boundary。
- **日志要服务 oracle**:只记录能判定状态的事件,例如 durable commit 前后、resource register/unregister、retry cursor、owner switch、root error chain。
- **每个 schedule 都有 control**:no-fault、failpoint-not-hit、safe sibling path 或 fixed revision。
- **命中后保留资产而非补丁**:沉淀为 selector、oracle、schedule template、注入点清单;实验改动可丢弃。

配套框架应该有四件东西:

| 组件 | 作用 |
|---|---|
| fault registry | 记录可用 failpoint/hook:位置、可控参数、触发条件、对应 obligation |
| schedule DSL | 描述 `fail-before/after`, `cancel`, `retry N`, `delay`, `owner-switch`, `topology-change` |
| trace collector | 收集 DDL job state、system table、日志事件、debug counter、error chain |
| oracle runner | 把 trace 和 SQL 结果判成 RED/GREEN/INVALID,并输出最小复现 |

这才是更完整的 AI-native fuzz:AI 不只是“生成输入”,而是根据源码自动搭一个能打断系统、观察系统、裁决系统的实验闭环。真正的难点也在这里:如何让 AI 可靠地选注入点、临时改代码、构造 schedule,并避免把 harness 问题误判成产品 bug。

### 6.7 资产复用:把 LOOP 变成增量系统

如果每一轮都重新读源码、重新想 oracle、重新写场景,那只是一次性 agent 能力,不是方法论复利。LOOP v2 应该有一个长期资产库,可以放在 TiDB Cloud / TiDB / Postgres 这类关系数据库里。核心目标:

```text
每轮挖掘 = 从资产库取候选 + 补一小段新分析 + 跑增量实验 + 回写新资产
```

资产库至少要存 7 类对象:

| 资产 | 作用 |
|---|---|
| module_profile | 每个模块的对象模型、状态机、常见 side effects、已有 harness 能力 |
| obligation_card | 已识别的 P/Q/F 或 boundary card |
| oracle | 可复用判定器:适用 bug 类、输入、输出、blind spot、可信等级 |
| scenario | 可复用场景:SQL workload、数据规模、对象拓扑、前置状态 |
| fault_point | failpoint/hook/log/counter 注入点:位置、触发条件、对应 boundary |
| schedule_template | success/fail-before/fail-after/cancel/retry/owner-switch/topology-change 模板 |
| run_result | 每次实验结果:RED/GREEN/INVALID、trace、最小复现、关联 bug/root cause |

最小 schema 可以先长这样:

```sql
CREATE TABLE module_profile (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  module VARCHAR(128) NOT NULL,
  subsystem VARCHAR(128),
  source_paths JSON,
  state_model JSON,
  known_side_effects JSON,
  harness_capabilities JSON,
  notes TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE obligation_card (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  module_id BIGINT NOT NULL,
  kind ENUM('proof','boundary') NOT NULL,
  selector VARCHAR(128),
  p_check TEXT,
  q_claim TEXT,
  d_dims JSON,
  boundary JSON,
  side_effects JSON,
  consumers JSON,
  consequence VARCHAR(128),
  confidence INT,
  status ENUM('candidate','validated','retired') DEFAULT 'candidate',
  evidence JSON,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE oracle (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(128) NOT NULL,
  obligation_kind VARCHAR(64),
  detects JSON,
  blind_spots JSON,
  inputs JSON,
  verdict_schema JSON,
  trust_level ENUM('hypothesis','used','llm_verified','trusted','refuted') DEFAULT 'hypothesis',
  evidence JSON,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE scenario (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  module_id BIGINT NOT NULL,
  name VARCHAR(128) NOT NULL,
  workload JSON,
  setup_sql TEXT,
  data_shape JSON,
  topology_shape JSON,
  min_cost JSON,
  tags JSON,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE fault_point (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  module_id BIGINT NOT NULL,
  name VARCHAR(128) NOT NULL,
  source_anchor VARCHAR(512),
  injection_type ENUM('failpoint','hook','log','counter','assertion','topology') NOT NULL,
  trigger_condition JSON,
  boundary_selector VARCHAR(128),
  patch_status ENUM('existing','temporary','proposed') DEFAULT 'temporary',
  notes TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE schedule_template (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(128) NOT NULL,
  applies_to_selector VARCHAR(128),
  steps JSON,
  controls JSON,
  expected_observations JSON,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE run_result (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  obligation_id BIGINT,
  oracle_id BIGINT,
  scenario_id BIGINT,
  fault_point_id BIGINT,
  schedule_id BIGINT,
  code_ref JSON,
  verdict ENUM('RED','GREEN','INVALID','INFO') NOT NULL,
  trace_ref TEXT,
  minimized_repro TEXT,
  bug_id VARCHAR(64),
  root_cause_id VARCHAR(128),
  lessons JSON,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

每轮 LOOP 的增量方式:

```text
1. 选模块后,先查 module_profile 和历史 run_result,不要从零开始。
2. 用 selector 查 obligation_card:哪些 boundary 已验证,哪些只是 candidate。
3. 查 oracle:有没有现成强 oracle;没有才做 oracle mining。
4. 查 scenario/fault_point/schedule_template:优先复用已有 workload 和注入点。
5. 只对本轮新增的 gap 写少量实验补丁或场景。
6. 跑完后把 RED/GREEN/INVALID 都入库;GREEN 也重要,它会降低下一轮重复探索。
7. 命中 bug 后,不要只写 bug draft;还要更新 selector、oracle blind spot、schedule template。
```

资产库的关键不是“存很多测试”,而是存**为什么这个测试值得跑**:

```text
selector -> obligation -> scenario -> fault schedule -> oracle -> result -> lesson
```

这样下一轮 AI 可以问数据库:

```text
这个模块已有哪个 selector 命中过?
有哪些 oracle 是 TRUSTED,哪些只是 HYPOTHESIS?
哪些 fault point 已经证明能 hold 住目标边界?
哪些 GREEN 说明这个方向别再浪费?
哪个历史 miss 还没有 test_discoverability 标注?
```

这会让 AI-native fuzz 从“每轮重新聪明一次”变成“越挖越有资产”的系统。

### 6.8 资产库增量 LOOP 首次验证(2026-07-10)

已用 `issue59055/fix59157` 做了一次最小实证。选择它的原因是:它正好属于 6.2 里旧方法覆盖不好的 `S-STATE / durability boundary` 类,必须靠临时注入 TiDB 内部 COMMIT failure 才能给出强 oracle。

本轮没有把资产库直接上 TiDB Cloud,而是先做本地 SQLite 原型,避免把存储接入成本和方法论验证混在一起。原型在 `/Users/bba/pc/ai-native-assets/`:

- `schema.sql`:资产、资产版本、资产关系、run_result、run_asset。
- `store.py`:导入 JSONL、按 `module + selector` 生成实验 pack、查询统计。
- `issue59055-seed.jsonl`:7 个资产,其中 4 个复用方法论资产,3 个本轮新增目标资产。
- `issue59055-results.jsonl`:vulnerable/fixed 两条 RED/GREEN run。
- `issue59055-promote.jsonl`:把被 RED/GREEN 验证过的 oracle/obligation/fault 提升到 `execution_verified`。

pack 结果:

```text
query: module=ddl/notifier, selector=DURABLE_BEFORE_ACK
asset_count: 7
reused methodology assets: 4
new target-analysis assets: 3
open_gaps: []
prior_runs: RED=1, GREEN=1
```

实验结果:

| revision | verdict | evidence |
|---|---|---|
| `c34a6b69f66ed080bfd4938ae51e134fc70b917d` vulnerable | RED | `handler_calls=1 commit_attempts=1 fault_hits=1`,progress COMMIT 失败后事件未重投递 |
| `0fdb32530d6fb5e810632ea72ef055daf8cda967` fixed | GREEN | `handler_calls=2 commit_attempts=2 fault_hits=1`,同一事件被重投递 |

这轮验证的核心结论:

```text
资产库要存 reasoning chain,不是存 test file。
稳定可复用的是 selector/oracle/scenario/schedule。
每个新模块仍要补 module profile / boundary obligation / concrete fault point。
RED 只能证明"有问题";RED+GREEN 才能提升 oracle 信任。
INVALID 要入库,因为它记录 harness 前置条件,防止下轮重复踩坑。
```

更重要的是,它让下一轮的效率指标变得可量化:

```text
reuse_ratio        本轮复用资产 / 总资产
new_asset_count    本轮为了目标新增多少资产
open_gap_count     pack 还缺哪些资产类型
invalid_rate       harness/env 问题占比
time_to_verdict    从 pack 到 RED/GREEN/INVALID 的时间
```

因此资产库的第一阶段不应该追求 schema 完美,而应该追求每轮都能回答:

```text
这次命中/未命中,下轮到底少想了什么?
哪个 oracle 因 RED/GREEN 变得更可信?
哪个 GREEN 方向应该降权?
哪个 INVALID 是 harness 债务?
```

因此,对“现在的测试方法论能不能发现那些 bug”的回答是:

```text
能发现一部分,尤其是 SQL_ONLY / SOURCE_ONLY + strong oracle 的 bug。
能补强后发现一部分,尤其是 S-STATE/S-LIFE/S-ERR 类 failpoint bug。
不能声称覆盖 S-ENV/STRESS_PERF 类,除非先建设对应 harness。
```

### 6.9 资产库增量 LOOP 第二次验证(2026-07-10)

第二个验证对象是 `issue53843/fix53849`:cancel add index 时 ingest writer/engine cleanup race。它属于 6.2 里的 `S-LIFE / lifecycle exactly-once` 类,是 PR review baseline 里更容易漏掉、但测试方法论应该能通过时序 oracle 抓到的形状。

系统不是重新从零开始分析。`target_queue` 先选择 `issue53843`,然后只补三个目标资产:

- `module.ddl-ingest.v1`
- `obligation.ddl-ingest.unregister-cleanup.v1`
- `fault.ddl-ingest.cancel-after-engine-open.v1`

复用资产是:

- `selector.lifecycle-exactly-once.v1`
- `oracle.no-leak-after-cancel.v1`
- `scenario.long-operation-cancel-window.v1`
- `schedule.cancel-active-resource.v1`

执行时把 broad oracle 收窄成可确定验证的 root-boundary oracle:`concurrent UnregisterEngines must close each opened engine exactly once`。结果:

| revision | verdict | evidence |
|---|---|---|
| `cc127c14b8cc9887b1be946baa2f220690722c63` vulnerable | RED | `close_calls=2 cleanup_calls=1`,第二个 unregister 在第一个 cleanup 未结束时重复 close 同一个 engine |
| `9c500ad9cb52c72372ad9d82f2a72190788d9478` fixed | GREEN | `close_calls=1 cleanup_calls=1 remaining_engines=0` |

本次新增 `/Users/bba/pc/ai-native-assets/issue53843-results.jsonl`,并把 `oracle.concurrent-unregister-exactly-once.v1` 标记为 `execution_verified`。同时,`oracle.no-leak-after-cancel.v1` 仍保留为 `hypothesis`,因为完整用户流 `ADD INDEX + ADMIN CANCEL DDL JOBS` 还没有在 testbed/failpoint E2E 层跑完。

这次验证比首例更能说明“演进系统”的价值:

```text
首例 issue59055 证明资产库能记住一个成功 replay。
第二例 issue53843 证明资产库能选择下一个目标,迁移 selector,补齐缺口,执行 RED/GREEN,再反过来拆分 oracle scope。
```

这也是后续自动化的关键经验:promotion 必须按实际证据粒度做,宁可新增窄 oracle,也不要把一个 broad oracle 过早升信任。

真正要做的 held-out 不是再跑 PR review,而是给 82 个 case 标注 `test_discoverability`,然后抽样验证:

```text
intro revision + generated test should fail
fix revision/current revision + same test should pass or become obsolete
```

这才是测试方法论的召回率。

### 6.10 资产库增量 LOOP 第五次验证(2026-07-10)

第五个验证对象是 `issue62424/fix62607`:事务内执行 `CREATE/ALTER INDEX` 后,DDL 隐式提交了旧事务,但 GC minStartTS observer 仍可能信任 processlist 里的旧 `CurTxnStartTS`,导致 GC safepoint 被长 DDL 阻塞。

系统仍然不是从零开始。`target_queue` 选择 `issue62424` 后,只补三个目标资产:

- `module.gc-ddl-transaction.v1`
- `obligation.gc-ddl-transaction.ignore-ddl-queued-startts.v1`
- `fault.gc-ddl-transaction.ddl-queued-stale-curtxnstartts.v1`

复用资产是:

- `selector.implicit-commit-state-cleanup.v1`
- `oracle.no-stale-txn-state-after-ddl.v1`
- `scenario.ddl-inside-transaction-gc-observe.v1`
- `schedule.ddl-implicit-commit-then-gc-check.v1`

执行时把 broad oracle 收窄成可确定验证的 root-boundary oracle:`queued DDL session's stale CurTxnStartTS must not be used as reported minStartTS`。结果:

| revision | verdict | evidence |
|---|---|---|
| `0501de48c5b033f17f300960ecfe4f40f9bc1742` vulnerable | RED | upstream `TestDDLInsideTXNNotBlockMinStartTS` 在 `integration_test.go:279` 失败,`GetMinStartTS()` 一直不能变成后续真实事务的 `tkTs` |
| `e9e8a04fe71611ed08ebfcf0755993812a07c521` fixed | GREEN | 同一 upstream 测试通过,`ReportMinStartTS` 跳过 `StmtCtx.IsDDLJobInQueue` 的会话 |

本次新增 `/Users/bba/pc/ai-native-assets/issue62424-results.jsonl`,并把 `oracle.ddl-minstartts-ignores-queued-ddl.v1` 标记为 `execution_verified`。同时,`oracle.no-stale-txn-state-after-ddl.v1` 仍保留为 `hypothesis`,因为完整 live-cluster GC safepoint 推进还没有在 testbed 层证明。

这次验证补上了一个重要能力:AI 不只是能找“明显的错误注入/资源泄漏”,也能从源码里抓出更隐蔽的 observer trust edge:

```text
P: observer sees CurTxnStartTS
Q: CurTxnStartTS means active transaction
F: queued DDL after implicit commit can carry stale CurTxnStartTS but is not an active transaction
```

这说明 LOOP v2 的前半段要继续强化“谁消费了这个状态”的追问。很多高质量 bug 不在状态写入点本身,而在另一个后台系统把 stale state 当成 safety proof 的地方。

### 6.11 从 validated queue 到 refill target(2026-07-10)

五个 historical target 都验证完成后,资产库第一次出现了“无 active target”的状态。这不是结束,而是系统化的下一个门槛:下一轮目标应该从 oracle debt / broad oracle gap 自动或半自动生成。

本次 refill 选择了 issue62424 的 broad gap:

```text
已验证窄 oracle: oracle.ddl-minstartts-ignores-queued-ddl.v1
仍是 hypothesis: oracle.no-stale-txn-state-after-ddl.v1
新 target: target.lift.issue62424.live-gc-safepoint.v1
新 obligation: obligation.gc-ddl-transaction.live-gc-safepoint-advances.v1
```

这一步还暴露了控制面 bug:如果 target state 只按 `module + selector` 聚合 run,同一 selector 下的新 obligation 会错误继承历史 RED/GREEN,被误判成 validated。`store.py` 已改成 target payload 中带 `obligation_key` 时按 obligation 绑定 prior runs。

随后 live-lift 在 testbed `8220955` 上完成 GREEN。实验没有等待完整 10m GC safe point 周期,而是读更直接的 consumer state:`/tidb/server/minstartts`。关键证据:

```text
DDL processlist TxnStart = 467568057103679489
sample 16: DDL still visible, minStartTS = 467568057116524554
sample 29: DDL still visible, minStartTS = 467568066213183509
sample 47: DDL still visible, minStartTS = 467568072111423519
```

这说明当前 fixed testbed 上 queued DDL 的 stale TxnStart 没有 pin 住 server minStartTS。broad `oracle.no-stale-txn-state-after-ddl.v1` 仍不应整体升为 trusted,因为这次只证明了 ADD INDEX + server minStartTS observer,不是所有 DDL 形态和完整 GC safe point cadence。

方法论结论:

```text
selector 是检索/聚类单位;
obligation 才是执行/验证单位;
oracle debt 是 refill 队列的主要来源。
```

### 6.12 自动 refill 后的 queue 形态(2026-07-10)

`store.py refill` 已经把上面的经验固化成资产库操作:扫描仍是 `hypothesis` 的 broad oracle,找到同 selector 下已经 RED/GREEN 验证过的窄 obligation,再生成下一轮 target 候选。当前自动生成了 3 个 refill target:

```text
target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
```

它们的共同状态是 `needs_target_analysis`,而不是 `validated` 或 `ready_to_execute`。这是有意设计的:refill target 只说明“这里有一个更宽的 oracle 债务值得继续追”,还没有具体到可执行的 P/Q/F、fault point 和 observer。

这次还补了第二个控制面约束:

```text
base_obligation = provenance,说明候选目标从哪个已验证窄 bug 长出来;
obligation_key = execution identity,只有它才能绑定 run 和判断 validated。
```

因此,带 `broad_oracle` 但没有 `obligation_key` 的 target,即使同 module/selector 下已有 RED/GREEN,也必须留在 `needs_target_analysis`。否则系统会把“历史窄 obligation 已验证”误读成“新 broad obligation 已验证”,资产库会过早关账。

当前 queue 的含义:

```text
validated targets: 6
candidate targets: 3
next at refill time: issue53843 no-leak-after-cancel refill
required next action at refill time: derive a concrete target-specific obligation_key before execution
```

这一步说明 LOOP 已经从“人手工挑下一个历史 bug”推进到“资产库根据 oracle debt 自动补充候选目标”。但自动化只负责召回,不负责替代证明义务分析;AI 的下一段价值正是把 broad oracle 压成小矩阵和强 oracle。

### 6.13 第一个 refill target 的 target analysis(2026-07-10)

issue53843 的 broad `oracle.no-leak-after-cancel.v1` 已经被压成一个新的可执行义务:

```text
target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
obligation: obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1
oracle:     oracle.ddl-ingest-cancel-terminal-no-live-resource.v1
fault:      fault.ddl-ingest.sql-cancel-after-local-engine-open.v1
state:      ready_to_execute
```

关键变化是 oracle 不再是“`ADMIN CANCEL DDL JOBS` 返回且表面没报错”。它必须同时证明:

```text
active_resource_window_hit=true
DDL job reaches an allowed terminal state
backend context / engines / opened writers are terminal
same engine UUID is not closed twice
no close-of-closed-channel or DDL worker panic log for this job
```

如果 cancel 没有打在 local ingest engine 已打开但还未自然 finish 的窗口,或者 harness 不能暴露 resource counters,这次执行只能记 `INVALID(harness)`,不能记 GREEN。

这就是 refill 阶段的核心收益:历史窄 bug 提供 base obligation,但新的 broad oracle 需要重新定义 P/Q/F。

```text
旧窄 P/Q/F:
  P: 两个 unregister overlap
  Q: 同一 backend context 的 cleanup 可以安全重复进入
  F: 没有 serialization 会让同一 openedEngine 被 close 两次

新 broad P/Q/F:
  P: SQL cancel 已经命中 local-ingest active resource window
  Q: terminal DDL state 意味着该 job 的资源也 terminal
  F: job terminal 只是控制面状态;backend/engine/writer 可能仍 live 或重复 cleanup
```

当前 queue 变为:

```text
validated targets: 6
ready_to_execute: 1
needs_target_analysis: 2
next executable: issue53843 SQL-cancel no-live-resource lift
```

随后完成了一次 current GREEN:

```text
run: run.issue53843.refill.current.13282a8.GREEN
test: TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource
command shape:
  make failpoint-enable
  go test -tags=intest ./pkg/ddl/ingest -run '^TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource$' -count=1 -v
  make failpoint-disable

observed:
  active_writes=64
  registered=1
  created_writers=2
  finish_calls=1
  live_engines=0
  live_writers=0
  closed_engines=1
  duplicate_closes=0
  disk_root_count=0
```

这次 GREEN 只证明 current/fixed 方向,不证明 bug 被这个 broad oracle 发现了。资产库因此把 target 推到 `needs_counterpart_run`,而不是 `validated`:

```text
validated targets: 6
needs_counterpart_run: 1
needs_target_analysis: 2
runs: RED=6, GREEN=7
```

新的方法论警示:AI 加 instrumentation 很有价值,但 instrumentation 不能 mask 原 bug。这里 enhanced mock backend 能证明 terminal no-live-resource,但要打 vulnerable RED 时,不能由 wrapper 自己提供安全 cleanup,否则会把 issue53843 的重复 cleanup race 藏掉。下一步 RED harness 必须让 vulnerable 的原 cleanup 语义暴露出来。

随后补了一条更窄但更强的 root-boundary RED:

```text
run: run.issue53843.refill.vulnerable.cc127c14.RED.memory-double-release
test: TestAINativeConcurrentUnregisterDoesNotDoubleReleaseMemory
observed: expected_current_usage=0, actual_current_usage=-2877, attempt=0
```

这个 RED 的价值不是“替代 SQL cancel RED”,而是把 `LIFECYCLE_EXACTLY_ONCE` 的 oracle 从 close/cleanup 调用次数扩展到 ownership ledger:同一批 engine 资源的 memory quota 也必须只 release 一次。它证明了一个改进点:生命周期类 bug 的强 oracle 应该同时覆盖 registry、resource handle、side-effect cleanup、quota/accounting 四个维度。资产库因此更新到 `runs: RED=6, GREEN=7`,但 refill target 仍停在 `needs_counterpart_run`;只有同一 `obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1` 的 vulnerable SQL-level RED 才能提升窄 oracle。

随后这个 SQL-level RED 已补齐:

```text
run: run.issue53843.refill.vulnerable.cc127c14.RED.sql-cancel-double-cleanup
test: TestAINativeIssue53843SQLCancelDoubleCleanupRED
observed:
  registered=1
  writes=1
  unregister_calls=2
  cleanup_ledger=-1
  cancelled=true
  alter result=ErrCancelledDDLJob
```

这次 harness 的关键是区分“触发路径”和“观察手段”:SQL cancel、DDL rollback、两个 cleanup owner 都由旧 TiDB 路径触发;observing mock backend manager 只把旧 `litBackendCtxMgr` 的非幂等 cleanup 语义暴露成计数器/账本。于是它不是完整真实 local-file leak replay,但足以作为 `obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1` 的 vulnerable RED counterpart。对应 current GREEN 已经证明 terminal mock resource clean,所以窄 oracle `oracle.ddl-ingest-cancel-terminal-no-live-resource.v1` 被提升为 `execution_verified`;broad `oracle.no-leak-after-cancel.v1` 仍保留为 `hypothesis`。资产库当时更新为 `runs: RED=7, GREEN=7`, `queue_states: validated=7, needs_target_analysis=2`,下一目标自动切到 S3 injected-error-identity refill。

S3 refill 后续已完成,并发现一个 current master 新 bug。它没有重放 issue48164 的旧 concurrent pipe error,而是把 broad `oracle.injected-error-identity-survives.v1` 收窄成 `obligation.external-storage-s3.multipart-failed-part-terminal-no-complete.v1`:multipart part1 成功、part2 `UploadPart` 注入失败后,`Close` 不得 `CompleteMultipartUpload` 一个 prefix-only partial object,且必须保留根错误。current `13282a8` RED 观测为 `writeErr=ai-native mock upload part failed,closeErr=<nil>,completeCalls=1,completedParts=1`;本地最小修复 GREEN 观测为 `abortCalls=1,closeErr=ai-native mock upload part failed`。资产库当前为 `runs: RED=8, GREEN=8`, `queue_states: validated=8, needs_target_analysis=1`,下一目标切到 `target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1`。方法改进:错误身份 oracle 对 storage writer 不应只看 final error text,还必须观察 terminal state action(`Complete` vs `Abort`)。

## 7. 下一步最高价值改进

不要把 29 个 NOT_FOUND 都平均处理。优先挑“高质量 correctness/lifecycle 且能形成 selector”的 miss:

1. `issue59055/fix59157` DDL notifier commit failure - 已完成首轮验证
   - 新 selector: durable commit boundary。
   - oracle: one-shot internal SQL COMMIT failure 后,要求 progress 不被视为 durable,下一轮能重投递。
   - 结果:vulnerable RED,fixed GREEN;同时验证了资产库增量 pack 的价值。

2. `issue53843/fix53849` ingest writer leak on cancel
   - 新 selector: lifecycle exactly-once on cancel/error。
   - 结果:已完成 root-boundary RED/GREEN;vulnerable `close_calls=2`,fixed `close_calls=1`。
   - oracle 状态:`oracle.concurrent-unregister-exactly-once.v1` 已 `execution_verified`;完整 no-leak-after-cancel E2E 仍是 hypothesis。

3. `issue48164/fix48163` S3 uploader overwrites real error
   - 新 selector: concurrent pipe/uploader error precedence。
   - 结果:已完成 RED/GREEN;vulnerable 后台有 `mock error` 但最终返回 `io: read/write on closed pipe`,fixed 最终保留 `mock error`。
   - oracle 状态:`oracle.concurrent-pipe-upload-error-identity.v1` 已 `execution_verified`;更宽的 injected-error-identity 仍是 hypothesis。
   - refill 新命中:同一 broad oracle 在当前 `pkg/objstore/s3store` 找到 multipart writer terminal-state bug;part2 `UploadPart` 失败后 `Close` 仍 `CompleteMultipartUpload` 1 个 prefix part 且返回 nil。本地最小修复改为 Abort + 返回根错误后 GREEN。窄 oracle `oracle.s3-multipart-failed-part-no-complete-preserve-root.v1` 已 `execution_verified`。

4. `issue51846/fix52315` PD leader network partition during add index
   - 新 selector: owner/topology handoff during long DDL。
   - 结果:已完成 root-boundary RED/GREEN;vulnerable retire 后同一 processing job 变 runnable, fixed 保持 non-runnable。
   - oracle 状态:`oracle.ddl-processing-id-survives-owner-retire.v1` 已 `execution_verified`;完整 allowed-state-after-topology-fault E2E 仍是 hypothesis。
   - 关键 P/Q:RetireOwnerHook fired 不等于旧 reorg worker 已退出;owner 退位再成为 owner 时必须保留 processingIDs,不能把同一个 ADD INDEX job 再派给第二个本地 worker。
   - 下一层 oracle: topology fault holds job in allowed state,then either resumes or reaches a clearly allowed terminal state,not duplicate-worker-driven canceling/rollback with ErrNotOwner。

5. `issue62424/fix62607` create index inside transaction may block GC
   - 新 selector: DDL implicit-commit path leaves transaction/session state that GC/process info trusts。
   - 结果:已完成 upstream integration RED/GREEN;vulnerable `ReportMinStartTS` 一直被 queued DDL session 的 stale `CurTxnStartTS` 卡住,fixed 跳过 `StmtCtx.IsDDLJobInQueue` 会话后通过。
   - oracle 状态:`oracle.ddl-minstartts-ignores-queued-ddl.v1` 已 `execution_verified`;完整 no-stale-txn-state-after-ddl / live GC safepoint E2E 仍是 hypothesis。

这五个比“再枚举更多 DDL syntax”更值钱,因为它们正好覆盖当前方法漏掉的高价值轴:S-STATE/S-LIFE/S-ERR/S-ENV/S-CFG。

## 8. 方法论结论

当前最高效方法仍然成立:

```text
先从源码/历史行为里找证明义务
再把它压成小矩阵
用强 oracle 验证红格
命中后暂停,反推 selector
```

但 GitHub held-out 要求把“证明义务”从 SQL/schema 语义扩成 DDL pipeline:

```text
schema/reference obligation
+ artifact generation obligation
+ state/durability obligation
+ lifecycle/error/retry obligation
+ external topology obligation
```

下一轮不应该问“还能不能多 fuzz 几条 DDL”,而应该问:

```text
这个 miss 属于哪个 obligation?
它缺的是 selector,还是 oracle,还是执行环境?
有没有一个 2x2 小矩阵或 failpoint 能把它变成低噪声红格?
```

这才是 GitHub 历史 bug 对方法论的真正价值:不是当 seed 变异,而是当盲点评测和 selector 训练集。

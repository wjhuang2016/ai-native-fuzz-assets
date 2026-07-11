# 高效 AI-Native DDL Bug 挖掘方法论(实战沉淀)
> 2026-06-30。来自一整轮"先低效 fuzz 0 命中 → 反思 → 推理驱动挖到 3 个真 bug"的复盘。负责人 wjhuang2016。

## 0. 核心论点(修正版):不是否定 fuzz,而是 fuzz 要被分析"喂"聪明
**失败的不是"fuzz",是"浅 fuzz"。** 最初的 fuzzer 0 命中,不是因为 fuzz 没用,而是生成的都是**平常 case**(标准/exotic schema + 标准 DML + 标准注入点)——现有 fuzzer 和人都能写、也覆盖了,**完全没体现 AI 的分析能力**。理论上 AI 来做随机测试应该**更强**,前提是生成被深度分析驱动。

**正确命题 =「分析 × 大规模 fuzz」,不是"推理取代 fuzz":**
- 分析给**深度**(打哪个非显然区域)、fuzz 给**广度和规模**(在那个区域大量跑)。
- 纯 fuzz 有广度无深度(平常 case);一次性手动推理有深度无规模(只查几个格子);**AI-native 的甜点 = 用深度代码/语义分析去塑造 fuzz 的搜索空间、生成策略、oracle,再大规模随机/系统化跑。**
- 本轮的矩阵差分(§3)就是"分析推导的搜索空间",但只**手动**查了几格——还没规模化。真正的 AI-native 形态:让 AI **自动**从代码枚举完整维度(所有"列被引用"特性 × 所有 ALTER 路径 × 组合),在这个分析推导的空间上**系统化 + 随机地大规模生成**,覆盖手动想不全的组合。
- AI 的独特价值落在三处,都是"喂给 fuzz"的:① 从代码分析出非显然的**生成维度/搜索空间**;② 推导出强 **oracle**(跨路径一致性、行为不变量,而非只 ADMIN CHECK);③ 从覆盖率/历史 bug 反馈**导引**搜索往脆弱区走。

实战对照(本轮真实数据,说明"浅 vs 被分析喂过"的差距):

| | 浅 fuzz(无分析) | 分析驱动(本轮手动版,尚未规模化) |
|---|---|---|
| 生成 | 标准/exotic schema + 标准 DML,平常 case | 从代码分析出的维度(特性×ALTER 路径)上构造 |
| oracle | 单一 ADMIN CHECK | 跨路径一致性 + 行为不变量(矩阵差分) |
| 产出 | 数百轮 **0 bug** | 数十个定向矩阵/差分测试 **3 个真 bug** |
| 下一步 | —— | **自动枚举维度 + 规模化 fuzz**(分析的深度 × fuzz 的规模) |

### 0.1 AI 高效发现 bug 的真正分工
AI 的优势不在"比 fuzzer 更会随机",而在把非结构化信息压缩成可执行搜索:

```text
代码/历史 bug/PR diff
  → 找薄弱目标
  → 抽证明义务或一致性义务
  → 生成反例空间
  → 设计高灵敏 oracle
  → 交给廉价执行器跑量
  → 命中后反思并改写下一轮搜索空间
```

本轮三个真 bug 说明这个分工是有效的:
- id1/id2 不是随机撞到的,而是 AI 从列引用代码里抽出"无 rewriter 则必须全路径 block"的判定式,再测判定式标红的格子。
- id30001 不是"多写几条 partial index SQL"撞到的,而是 AI 识别出 planner 代码里有一个证明义务:`query predicate => partial index predicate`,然后专门生成这个证明可能误判的反例。

效率公式可以写成:

```text
有效命中率
= 目标 bug 密度 × 证明义务精度 × oracle 灵敏度 × 执行吞吐 / triage 噪声
```

普通 fuzz 主要提高"执行吞吐";AI 要负责把前三项拉高。新 bug 的意义不是多一个 bug,而是证明某个目标选择/证明义务/oracle 组合能把有效命中率拉起来。每次命中后都要回答:这次到底提高了哪一项?还能不能继续提高?

## 1. 不要和现有 fuzzer 抢"饱和路径"
add-index / merge 这类被 fuzz 烂的路径,再多轮也挖不到不存在的 bug。**先识别测试覆盖薄的地方**:新特性、特性交叉、罕见组合、最近改动的代码。

## 2. 推理驱动定向(diff-directed),不是随机生成
- 读最近 N 周合入的 PR diff / 新特性 → 推理"这个改动在哪个边界/哪条路径没处理" → 构造**一个**精准测试。
- 新代码 = 未被 fuzz = bug 密度最高。
- 注意可达性:最近改动若在分布式/ingest/云存储路径(如 global-sort proxy job、cross-keyspace),in-process 单测够不到,需要真集群;**优先选 in-process 可达的新特性**(本轮选了 partial index)。

## 3. ⭐ 矩阵差分 oracle(本轮最有效的武器)
对一个**本应跨相关操作/路径一致**的属性,建矩阵 `{操作} × {依赖/输入}`,找**发散的格子** = bug。这是把差分测试用在**代码路径**上(而非数据),且天然推理友好(你只需推理"什么本应一致")。

实例(本轮 2 个 bug 都来自这一张表):
> 列 X 被特性 Y 引用,则 `{RENAME, CHANGE, MODIFY, DROP} COLUMN X` 都应一致地阻止/处理。

完整矩阵(本轮**逐格行为验证**,非假设;列引用特性 × ALTER 路径):

| 列引用特性 | RENAME | CHANGE-rename | MODIFY-type | DROP | 保护机制 | 结论 |
|---|---|---|---|---|---|---|
| CHECK 约束 | 阻 3959 | **静默丢 ❌** | 保留 | 阻(drop) | 无 rewriter | **bug#1**(数据完整性) |
| partial index 谓词 | **误 1054 ❌** | 阻 8272 | 阻 8272 | 阻 | 无 rewriter | **bug#2**(错误码) |
| generated col(STORED/VIRTUAL/多列) | 3108 | 3108 | 3106 | 3108 | 全路径 block | 一致 ✓ |
| 分区键 | 3855 | 3855 | — | 3855 | 全路径 block | 一致 ✓ |
| FK(子列 & 父列) | 改名 | 改名(仍 enforce) | block-incompat | 阻 | **rewriter** | 一致 ✓ |
| TTL 列 | 改名 | 改名 | type-check | 阻 | **rewriter** | 一致 ✓ |
| vector/columnar 索引列 | renameColumnTo | guard | guard | 阻 | 在 `idx.Columns` | 一致 ✓ |

**精确根因模型(本轮提炼,比"漏检查"更可预测)**:每个"列被引用"特性,在列被改名时只有两条正确出路——
- **(A) rewriter**:job handler 里有 `updateXxxWhenModifyColumn`,改名时把引用一起改(FK/TTL/普通&vector 索引列走这条)→ 改名成功且引用跟着走,全路径一致。
- **(B) 全路径 block**:无 rewriter,则每条 ALTER 路径都必须调依赖检查、阻止改名(generated/partition 走这条)。
- **bug = 既无 rewriter,又只在部分路径 block**(引用存在 `idx.Columns` 之外的表达式里 → 常规索引列处理够不到 → 必须靠手写 block,而开发者漏了一条路径)。CHECK 约束(modify 路径漏 block→静默丢)和 partial 谓词(rename 路径漏 block→误报)正是仅有的两个掉进这个缝的特性。

这个模型是一个**判定式**:给定任一(特性, 路径)格子,可直接预测它是否 buggy——本轮据此正确预测"哪 2 个 buggy、其余 5 个一致",并逐格行为验证(generated 跨 STORED/VIRTUAL/多列一致;FK/TTL 的 rewriter 实测 `pc→pcx→pcy`、`t1→t1x→t1y` 引用正确跟随)。**这比"找发散格子"更进一步:有了模型就不必盲扫,只测模型标红的格子。**

### 3.1 Proof-obligation fuzzing:专门攻击代码里的"证明器"
id30001 把方法论从"跨路径一致性矩阵"推进到另一类高密度搜索:找代码里的**证明义务**。这类代码通常长这样:

```text
如果 check(input, property) 返回 true,
系统就会跳过某些安全路径/选择某条优化路径/相信某个条件一定成立。
```

这时 AI 的任务不是枚举 SQL 语法,而是把 `check` 背后的语义命题翻译出来,再生成反例族:

```text
1. 找证明器:CheckConstraints / canUseX / implyY / isSafeZ / prune / rewrite / fast-path guard
2. 写出证明义务:前提 P 是否真的蕴含结论 Q?
3. 找反例形状:P 成立但 Q 不成立,或 Q 对 NULL/边界/OR/非等值不封闭
4. 设计差分 oracle:强制走 fast path vs 禁止 fast path,结果必须一致
5. 小矩阵确认后,再把反例形状规模化
```

partial index 命中的具体映射:
- 证明器:`partidx.CheckConstraints` 决定 partial index path 是否可用。
- 证明义务:`query predicate` 必须蕴含 `partial index predicate`。
- 反例形状:`a >= 0` 不蕴含 `a < 3`,但 range 证明错误接受。
- oracle:`IGNORE INDEX(pi)` 与 `USE/FORCE INDEX(pi)` 行集必须一致。

这类搜索特别适合 AI,因为代码里"证明器"很多,而随机 fuzz 不知道哪条 SQL 是在挑战证明器的逻辑边界。AI 可以读代码命名、调用上下文、历史 bug,把"可能不安全的证明"变成少量高价值反例。

### 3.2 绿色矩阵之后,找"同一义务的新入口"

id630014 说明一个很实用的升级:一个 owner 的基础矩阵变绿,不代表这个 owner 没价值;它可能说明**常规入口已经有 helper**,下一步要找的是"改变同一 ownership 维度、但没走 helper 的兄弟 DDL 入口"。

这次 masking policy 的路径就是:

```text
基础矩阵:
  rename/drop/truncate/column-change 都绿
  源码里能看到 masking-policy 专用 rewrite/cleanup/remap helper

反推新入口:
  找另一个会改变 table_id/name binding 的 DDL
  EXCHANGE PARTITION 会交换 standalone table ID 和 partition physical ID
  但 check/exchange path 没有 masking-policy remap/block

强 oracle:
  交换前 policy 可 DISABLE/ENABLE
  交换后 sys row 仍在,但 table_id 已变成 partition_id
  DISABLE/DROP by nt 和 by pt 都找不到旧 row
  重建同名 policy 只管理新 table_id,旧 row 继续 ENABLED
```

这个方法的关键不是"继续扫 masking policy",而是把绿样本转化成源码对照:哪些路径显式修了 owner?哪些 sibling path 改了同一 owner/key 却没调用修复函数?这种问题比随机扩语法高效得多。

## 4. Oracle 挖掘是方法论核心(DDL bug ≠ 只有一致性)
ADMIN CHECK 只抓"索引-数据不一致"。要挖**多种 oracle**,每种抓不同 bug 类:
- **一致性**:ADMIN CHECK / ADMIN CHECK INDEX
- **错误结果(metamorphic)**:加索引/特性不得改变任何查询结果(`USE INDEX` vs `IGNORE INDEX` 行集一致;谓词蕴含边界)
- **跨路径/跨操作一致性**:同一逻辑操作经不同语法/路径必同行为(§3 矩阵差分)
- **行为不变量(最硬)**:约束改前 enforce → 改后必仍 enforce(用"插入违反数据看是否被拒"验证,而非查元数据)
- **错误处理一致性**:同类逻辑违规应报同类错误码
- no-panic(mutation checker / -race / recover)、liveness(超时看门狗)、元数据往返(SHOW CREATE 可重放)
- **差分 vs MySQL / 文档语义**:语义偏差是 DDL bug 富矿

## 5. Bug 库当"模式推理"资产,不当"种子变异"源
- 低效:把 234 个回归测试的 repro 拿来小幅变异——离已有测试太近,在同样被覆盖的邻域打转。
- 高效:从历史 bug 抽取**开发者反复犯的错误模式**(如"新依赖只加了部分 ALTER 路径的检查"),再去**没被这样检查过的代码**里搜同模式。这是 cross-pollination,不是 repro 变异。

## 6. 严谨性(避免把 artifact 误报成 bug)
- **行为测试 > 元数据查询**:验证实际 enforce 与否,别只信 information_schema 计数。
- **唯一名 + 干净状态**:本轮差点把"CHECK 约束名数据库级唯一 → 多表复用 `chk` 致 CREATE 失败"误报成 2 个 bug,靠行为测试纠正。
- 命中后**根因定位**(grep 不一致的代码路径,确认是真 gap 不是设计)+ **差分确认**(同义操作/MySQL 对照)。

## 7. 元教训:harness 投在哪
本轮 ~90% 精力耗在 harness 工程,但跑的是**浅 fuzz**(平常 case)→ 0 bug;那 ~10% 的分析推理找到全部 bug。结论**不是**"少造 harness",而是:**harness 要为"分析推导出的搜索空间"服务,不要为"平常随机生成"服务**。
- 浪费的 harness = 给浅 fuzz 造的(随机 schema/DML、标准注入点)。
- 值得的 harness = 把"分析出的维度(特性×ALTER 路径×组合)+ 强 oracle(跨路径一致/行为不变量)"规模化跑起来的引擎。
- 先用最快手段(in-process testkit `go test -run` / 直连 SQL)秒级验证分析假设;假设被几个手测确认有 bug 信号后,再投 harness 把那块**规模化**。

## 8. Bug 聚集性:三步定向循环(本轮验证的元方法)
Bug 不均匀分布——**找到一个洞口,附近往往是一窝**(同一开发者、同一抽象、同一遗漏模式)。把"分析×fuzz"组织成一个可复制的聚集性循环:

1. **从 bug/PR 深挖潜在点**:不停在"复现这个 bug",而是抽出它的**根因模型**(本轮:列引用特性的"rewriter vs 全路径 block"判定式)。模型 = 一个能给任意新格子打"红/绿"的**判定式**,不是一句模糊的"附近"。
2. **简单搜索确认聚集**:用最快手段(直连 SQL / in-process testkit)秒级试几个模型标红的格子。命中即证明"这是一个结构性脆弱区,不是孤立 bug"。
3. **针对性 fuzz 规模化**:对模型标红的整片区域系统枚举 `{特性 × ALTER 路径 × 变体}`(变体 = 多列/inline/STORED-VIRTUAL/ALGORITHM=COPY/multi-schema…),逐格跑行为 oracle。规模覆盖**手查想不全的变体**。

本轮验证结论:三步成立,但**关键在第 1 步的"模型"质量**——有了判定式,第 3 步既能精确命中红格(bug#1 在 ALGORITHM=COPY、multi-schema、inline、多列 check 下全部静默丢,blast radius 比初记录大),又能用**阴性结果**证明绿格安全(FK/TTL 的 rewriter 实测生效、generated 全路径一致)——这是纯随机 fuzz 给不了的确定性。"附近的更多 bug"常是**同根因的更多变体/更大爆炸半径**,这本身就修正了 bug 的真实严重度与修复范围。

## 9. 标准循环(可复制,8 步细化)
```
1. 选靶:最近 PR / 新特性 / 特性交叉(避开饱和路径;优先 in-process 可达)
2. 推理:什么不变量本应成立?尤其问"此属性是否跨所有相关代码路径一致处理?"
3. 构造矩阵/差分测试:{操作/语法/路径} × {依赖/输入/边界}
4. 跑(快:in-process testkit 或直连 SQL,秒级反馈)
5. 任何发散/违背 = 候选 bug
6. 严格验证:行为测试(实际 enforce)+ 唯一名干净态 + 差分 vs MySQL/同义操作
7. 根因定位:grep 不一致的代码路径,确认真 gap
8. 提炼模式 → 反哺:同模式还可能出现在哪条路径/哪个特性
```

### 9.1 命中后暂停门(新增流程约束)
发现新 bug 后**不要立刻继续扩大搜索**。先暂停,把这次命中变成方法论资产,再决定下一轮是否继续挖同一区域。

暂停门最少产出 7 件事:
1. **最小复现**:从探针/随机命中收缩到能人工解释的一段 SQL 或一个测试。
2. **oracle 命名**:明确这次到底是哪类 oracle 命中,例如行为 oracle、路径一致性 oracle、planner 可用性 oracle。
3. **根因模型**:不是只写"漏检查",而要提炼成可预测其他格子的判定式。
4. **边界/阴性证据**:至少记录几个相邻但不命中的格子,避免把 bug 描述成过宽结论。
5. **生成空间修正**:把新模型转成下一轮可枚举维度,并删掉低信号维度。
6. **为什么 work**:明确这次命中提升的是目标密度、证明义务精度、oracle 灵敏度、执行吞吐,还是降低 triage 噪声。
7. **资产回填**:更新 `found_bug`、交接文档、方法论文档;必要时再写 issue/repro 草案。

只有暂停门完成后,才进入下一轮规模化枚举。这样做的目的不是放慢,而是防止"命中一个 bug 后继续盲扫",把最有价值的根因信息浪费掉。

### 9.2 DDL-only 纠偏:reference ownership matrix
当前下一轮不继续扩 optimizer/executor proof family,而是回到 DDL 本体。目标不是"写更多测试",而是把本轮最有效的矩阵差分方法做成 DDL 引用所有权搜索:

```text
DDL 改对象后,所有引用必须 rewrite 或 block
```

新的工作资产:
- `/Users/bba/pc/ai-native-ddl-reference-matrix.md`: owner/path/oracle/优先级矩阵。
- `/Users/bba/pc/ai_native_ddl_reference_matrix_probe.py`: 小矩阵探针,跑已知红格 control、block control、rewrite control。

2026-07-01 小矩阵结果:
- 28 格子,`findings=0`,`known_controls=3`,`skipped=0`。
- 已知红格复现:CHECK + CHANGE 静默丢;CHECK + multi-schema CHANGE 静默丢;partial-index predicate + RENAME 报误导性 1054。
- 绿格确认:generated/partition/FK/TTL/ordinary index/global index 的基础 rewrite/block 行为与 owner 模型一致。
- 方法论修正:block oracle 不能只看 `rc != 0`,还要看错误族是否来自目标 owner。FK parent drop 第一版被"只剩一列"拦住,后来改成父表保留额外列,确认真正命中 FK 保护。

这轮要刻意防漂移:
- 查询结果只能当 DDL 后验行为 oracle,不能把主线切到 planner/executor。
- id30002 作为方法论旁证保留,但不作为当前搜索方向。
- 新 bug 命中后立刻停,回答"哪个 owner/path 判定式被验证了,为什么 AI 能提前标红这个格子,下一轮如何更新矩阵"。

### 9.3 Object-reference:从列名引用扩到对象引用
列引用矩阵的第一轮阴性结果不是"没有东西可挖",而是说明简单列 owner 的 rewrite/block 已经比较稳。下一步应该换维度,继续同一个证明义务:

```text
DDL 改对象后,所有引用必须 rewrite 或 block
```

但这里的"对象"换成:
- placement policy 被 DB/table/partition 引用。
- partition DDL 改变 table/partition physical ID 和 global/local index 状态。
- global index cleanup 依赖 delete-range 与旧 index ID 记录。

新的探针:
- `/Users/bba/pc/ai_native_ddl_object_reference_probe.py`
- `/Users/bba/pc/ai_native_ddl_stateful_object_probe.py`:failpoint-backed rollback/cancel 窗口探针。
- `/Users/bba/pc/ai_native_ddl_masking_policy_reference_probe.py`:side sys-table reference owner 探针。

这个探针刻意不用宽 fuzz,只覆盖两类高密度格子:
1. **placement policy refs**:drop in-use policy 必须 block;table/partition placement 改写后旧 policy 必须可 drop,新 policy 必须继续 in-use;remove partitioning 必须释放 partition policy 但保留 table policy。
2. **global/local index refs during partition DDL**:缺 GLOBAL 必须 8264 block;`UPDATE INDEXES`/`REMOVE PARTITIONING` 必须改写 `SHOW CREATE` 里的 global/local 状态;`ADMIN CHECK` 和 index/table 行集必须一致;exchange partition 遇到 global index 必须 1731 block。

方法论上的关键变化:
- oracle 不再只看列名是否出现在 `SHOW CREATE`;要看"旧引用是否解除、新引用是否仍被 owner 保护"。
- block case 必须校验错误族,否则会把前置 guard 当成目标 owner 的正确保护。
- 查询行集只作为 DDL 后验 oracle:用来验证 global index metadata rewrite 后的数据可见性,不是把主线切去 executor。

2026-07-01 object-reference 小矩阵结果:
- 17 格子,`findings=0`,`skipped=0`。
- placement policy:table/partition in-use drop 均 `8241` block;table/partition placement rewrite 后旧 policy 可 drop,新 policy 仍 in-use;remove partitioning 释放 partition policy 但保留 table policy;placement + remove partitioning multi-schema 以 `8200` block;`ALTER PLACEMENT POLICY` 更新 settings 后 table/partition dependent refs 均仍 in-use。
- partition policy 状态变化:drop partition 释放被 drop partition 的 policy;truncate partition 保留该 partition policy,且 p0 仍可写。
- global/local index:缺 required global 以 `8264` block;`UPDATE INDEXES` 能把 `idx_a` 改 global 且保持 `idx_b` local;remove partitioning 清掉 `GLOBAL` 与 `PARTITION BY`;exchange partition 遇 global index 以 `1731` block;drop/truncate partition 后 `ADMIN CHECK` 与 global-index/table 行集一致。
- 混合格子:同一个 `REMOVE PARTITIONING` 同时带 table/partition placement refs 与 global index 时,partition policy 被释放、table policy 保留、global marker 清除、行集一致。

2026-07-01 masking-policy side-metadata 小矩阵结果:
- 13 格子,`findings=0`,`skipped=0`。
- 覆盖 table rename / cross-DB rename / multi-table rename,`mysql.tidb_masking_policy` 的 `db_name`、`table_name` 跟随 table ID rewrite。
- 覆盖 column rename 与 multi-schema `CHANGE COLUMN ... ADD COLUMN`,系统表里的 `column_name` 与 expression 一起 rewrite。
- 覆盖 supported/unsupported `MODIFY COLUMN`:支持类型保持绑定,不支持类型 block 且 policy 不变。
- 覆盖 drop column/table/database cleanup,以及 truncate table 后 `table_id` rewrite 且 policy 仍可 disable/enable。
- 方法论含义:这类 owner 看起来高危,但它有统一 side-table helper,且 no-reorg/reorg modify-column 两条完成路径都调用 sync helper,所以基础 rewrite/cleanup 层应降权。阴性结果同样有价值:它把下一轮筛选标准收紧到"side metadata + 多入口 + helper 缺失或只在部分状态调用"。

### 9.4 Stats side-metadata:新红格证明筛选规则有效
masking-policy 绿格之后,下一步不是继续在同一 owner 上堆 case,而是按同一个 DDL 证明义务去扫新的 side metadata owner:

```text
DDL 改对象后,所有引用必须 rewrite 或 block
```

stats 是一个更高价值的 owner,因为它同时具备三个信号:
- `mysql.stats_*` 存储按 table/partition/column/index ID 关联,不是纯名字关联。
- `SHOW STATS_META` / `SHOW STATS_HISTOGRAMS` 对用户暴露 db/table/partition/column/index name。
- DDL subscriber 会处理一部分 ID rewrite 或 stats 初始化,但展示层还依赖 stats cache 里的 `statistics.Table` 对象。

小矩阵结果:
- 新增 `/Users/bba/pc/ai_native_ddl_stats_reference_probe.py`,覆盖 7 格,结果 `SUMMARY total=7 findings=2 skipped=0`。
- 5 个绿格说明 owner 不是整体坏:table rename 可见 stats 跟随新表名;add/remove partitioning 会 rewrite global stats table ID;truncate table 创建新 table ID 的空 stats;truncate partition 更新 global/partition visible counts。
- 2 个红格同属一个根因族:`ANALYZE TABLE` 后执行 `RENAME COLUMN a TO aa` 或 `CHANGE COLUMN a aa INT`,live schema 与 `SHOW CREATE TABLE` 都只有 `aa`,但 `SHOW STATS_HISTOGRAMS` 仍显示旧列名 `a`,直到重新 `ANALYZE TABLE`。
- 草案:`/Users/bba/pc/ai-native-stats-column-rename-draft.md`。

为什么这次能 work:
- 不是随机扫 `SHOW STATS_*`,而是先从代码结构识别"ID-keyed storage + name-exposing API + async/cache subscriber"这一类 owner。
- 先跑 table rename、partitioning、truncate 等阴性控制,证明 stats owner 的 ID rewrite 大体存在;这样 column rename 红格更像局部缺口,不是测试环境或 stats 系统整体延迟。
- 主动排除了高噪声 oracle:drop index/drop column 后 stats 残留可能是 delayed stats GC 的设计行为,不能直接当 bug。column rename 不同,列还活着且 ID 可解析,展示旧名就是更干净的 stale reference 信号。
- 根因链条比"缓存旧了"更具体:`CHANGE/RENAME` 触发 `ActionModifyColumn`,但已有 analyzed histogram 时 `InsertColStats2KV` 的 `insert ignore` 是 no-op,不会推进 `stats_meta.version/last_stats_histograms_version`;stats cache refresh 只扫 version 变大的表,所以 `TableInfo.UpdateTS` 的 schema-change reload 保护没有机会触发。

新的判定式:

```text
side metadata keyed by object ID
+ public SHOW/API exposes object name
+ DDL path does not advance the version/invalidation signal that refreshes cached display metadata
= DDL rename 后的 stale visible reference
```

这次命中把下一轮筛选标准从"side metadata + 多入口 + helper 缺失"继续收紧成:
- 优先找同时保存 ID 与 name,或按 ID 存储但按 name 展示的 owner。
- 优先找 DDL 事件只更新 storage、不推进 cache/API invalidation 或版本信号的路径。
- 每个新 owner 都先用 3-5 个阴性控制证明 oracle 不是延迟异步噪声,再把红格最小化。

暂停门结论:
- stats column rename 已完成到可讨论质量:最小复现、`RENAME`/`CHANGE` blast radius、预期语义、源码锚点、根因假设和 issue-ready body 都在 `/Users/bba/pc/ai-native-stats-column-rename-draft.md`。
- 不再扩更多 stats 格子;下一步如果继续搜索,应带着这个新判定式去找新的 DDL side-metadata owner,而不是在同一 owner 里堆变体。

### 9.5 privilege grant side-metadata:负样本也要改 selector
stats 命中后,第一条新的 sys-table 线索是 privilege grant:`mysql.tables_priv` / `mysql.columns_priv`。它表面上很像高风险 owner:
- 系统表里有 `DB`、`Table_name`、`Column_name`。
- `SHOW GRANTS` 会把这些名字暴露给用户。
- DDL table/column rename 没有对应的 rewrite helper。

但这个 owner 不能直接套"DDL 改对象后引用必须 rewrite 或 block"。原因是 privilege grant 首先是**名字绑定的用户 policy**,不是对象身份引用。筛选探针 `/Users/bba/pc/ai_native_ddl_privilege_reference_probe.py` 跑 3 格:

```text
SUMMARY total=3 findings=0 skipped=0
```

关键行为:
- table grant 在 `RENAME TABLE t TO t2` 后仍留在 `t`;grantee 不能访问 `t2`,但 rename 回 `t` 后 grant 重新生效。
- table grant 在 `DROP TABLE t` 后不消失;重新 create 同名 `t` 后 grant 重新绑定。
- column grant 的 `mysql.columns_priv` / `SHOW GRANTS` 在 `RENAME COLUMN a TO aa` 后仍显示 `a`;再加一个新列 `a` 后,授权文本仍绑定这个名字。

这轮没有发现 bug,但改进了筛选规则:

```text
sys table has db/table/column strings
!=
DDL owns object-identity reference
```

新的前置问题:
1. 这个 metadata 是对象身份引用,还是用户故意配置的名字 policy?
2. DDL rename/drop 后,同名新对象重新出现时,旧 metadata 是否按设计重新绑定?
3. public API 暴露旧名字时,这是 stale object reference,还是 policy 的文字表达?

只有通过这三问,才值得进入 rewrite/block matrix。否则就是假高信号,会把方法带偏。

### 9.6 table-cache side-metadata:容器级 DDL 漏掉 sibling path 的 block/cleanup
privilege 负样本之后,新的 selector 要先证明 object-identity binding。`mysql.table_cache_meta` 正好满足:
- 系统表按 `tid`(table ID)存储,不是按名字存储。
- `ALTER TABLE ... CACHE` 会写入 `mysql.table_cache_meta`。
- `ALTER TABLE ... NOCACHE` 会清理这个 table ID。
- cached table 的状态通过 `SHOW CREATE TABLE` 暴露为 `/* CACHED ON */`。

小矩阵 `/Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py` 跑 3 格:

```text
SUMMARY total=3 findings=1 skipped=0
```

绿格控制先证明 owner 模型:
- `CACHE` 创建 table-id side row,`NOCACHE` 删除 side row。
- cached table 对直接 table/index/partition DDL 都 block:rename/drop/truncate/add-index/rename-index/partitioning 均报 cache-table error family,且失败后 side row 不变。

红格:
- cached table 所在 schema 执行 `DROP DATABASE` 时,DDL 静默成功。
- table 已从 `information_schema.tables` 消失。
- 但 `mysql.table_cache_meta` 仍残留旧 `tid`。
- 草案:`/Users/bba/pc/ai-native-table-cache-drop-database-draft.md`。

这次为什么 work:

```text
object-identity side metadata
+ sibling DDL paths already have explicit block/cleanup controls
+ broader container DDL removes the object through a different path
= orphan side metadata after DDL
```

这和 privilege 负样本刚好形成对照:
- privilege:名字 policy,rename/drop 不 rewrite 不能算 bug。
- table-cache:table ID policy,table object 消失后 side row 没消失就是 stale object reference。

新的搜索规则:
1. 先证明 owner 是 object-identity binding。
2. 再找 sibling DDL 中已经存在的 block/cleanup 规则。
3. 优先打 broader container path:`DROP DATABASE`、multi-table DDL、partition reorg、truncate/replace-ID 这类绕过单表入口的路径。
4. 命中后立即暂停,不要继续扩 owner 内变体;先问修复语义是"跟 sibling path 一样 block"还是"允许但 cleanup"。

当前目标卡:

```text
目标:
  把 table-cache / DROP DATABASE 红格做成完整案例,验证 selector 是否真能预测 DDL bug。

案例:
  /Users/bba/pc/ai-native-id30004-method-case.md

不是目标:
  继续扩 table-cache 变体,或转去执行器/查询层寻找新错结果。

要回答的问题:
  1. 这个 bug 为什么能被 AI 提前标红?
  2. 现有代码语义更支持 block 还是 cleanup?
  3. 这个 selector 下一轮应该打哪个 DDL owner,哪些 owner 要降权?
```

窄源码校验后的倾向:
- direct table/index/partition DDL 都显式 `ErrOptOnCacheTable` block cached table。
- `ALTER TABLE NOCACHE` 是唯一正式解除 cache 状态并删除 `mysql.table_cache_meta` 的路径。
- 因此更一致的修复语义是 `DROP DATABASE` 先扫描 schema 内 table,遇到 cached table 就 block;如果 owner 决定允许 drop schema,则必须在 `ActionDropSchema` final state 按 dropped table IDs 批量清 `table_cache_meta`。

此前 object/masking/privilege/table-cache 阴性与红格结果的价值:
- 说明普通 object-reference happy path 不是当前最高密度区域。
- 也证明 oracle 设计是可迁移的:从列 owner 的"列名 rewrite/block"升级成对象 owner 的"旧引用解除/新引用受保护/后验行集一致"。
- privilege 负样本补上了 selector 的另一面:不是所有 sys-table name 都是 DDL 引用;必须先证明 object-identity binding。
- table-cache 红格补上了 selector 的另一面:已经有 sibling path guard 的 owner,最该优先打 broader container path。
- 下一步不应继续堆 table placement、普通 drop/truncate partition、普通 placement policy update、简单 global index 组合、stats rename 变体、privilege grant rename/drop 变体或 table-cache 变体,而应先完成 table-cache 暂停门/owner 讨论,之后再转向新的 DDL side-metadata owner,尤其是"ID-keyed storage + name/API display + version/invalidation signal"或"sibling block/cleanup + container bypass"这一类。

### 9.7 next-owner scan:coverage gate 和 oracle 噪声也是方法论
id30004 之后没有直接继续跑 SQL,而是先做 next-owner scan:

```text
/Users/bba/pc/ai-native-ddl-next-owner-scan.md
```

这一步的关键结论:
- `ATTRIBUTES` / PD label rules 很像 table-cache:有独立 label rule,有 table/partition 身份,rename/truncate/drop/recover/flashback 都需要 rewrite/cleanup。但它已经有很强的现有覆盖:rename、多表跨库 rename、truncate、recover、flashback、drop table、drop/recreate、drop database、drop/truncate/exchange partition。它应作为 coverage gate 的正例,不是下一轮新 bug 高密度区。
- TTL job status/task 也是 ID-keyed side metadata,但清理职责在 TTL worker 和后台 GC。除非先找到确定性 cleanup trigger,否则"DDL 后立刻看到旧 row"只是 async cleanup 噪声,不能直接当红格。
- index usage 现在线上 public surface 主要是 in-memory `information_schema.tidb_index_usage`,旧的 `mysql.schema_index_usage` 已由升级路径删除;这更像 runtime usage tracking,不适合作为 DDL owner 主线。
- region split policy 是新的负样本:`SHOW CREATE TABLE` 会暴露 table/index split hint,但 policy 挂在 `TableInfo`/`IndexInfo` 内部,不是独立 side metadata。5 格探针验证 rename index、drop index、drop+add index、change column type、cross-schema rename table 均无 stale reference;它应降权为"object-local property 自然跟随"。
- stats lock/analyze-options/column-usage 仍然高信号,但它属于 id30003 已经命中的 stats owner family;在 owner 反馈或 fix validation 前不继续扩 stats 变体。

方法论更新:

```text
高结构相似度 != 立刻可跑矩阵。
下一轮 live matrix 需要同时满足:
  object-identity side metadata
  + 未被现有测试覆盖的 container/state DDL 入口
  + 低噪声 post-DDL oracle
```

这能防止 AI 看到一个 sys table、ID 字段或 `SHOW CREATE` hint 就扩 fuzz,把工作带回"随便试"。负样本不是停滞,而是在提高 selector precision。

2026-07-02 region split policy 负样本补了一条更细的规则:

```text
SQL-visible metadata
+ 存在 DDL action
!=
独立 reference owner

如果 metadata 只是 TableInfo/IndexInfo 的内嵌属性,
rename/drop/跨库 rename 会自然跟随对象生命周期,
除非另有 side cache / async record / version invalidation 层。
```

这轮小矩阵没有新 bug,但改善了 AI 搜索效率:下一轮不要再把"SHOW CREATE 有一段持久 hint"直接当成 side-owner,要先问它是不是独立存储、独立刷新、独立清理。

### 9.8 sequence default reference:表达式 owner 也可以引用 DDL 对象
region split 负样本之后,新的源码筛选没有继续盯 `SHOW CREATE` hint,而是找"schema expression 引用另一个 DDL object"。sequence default 正好满足:

```sql
CREATE SEQUENCE seq;
CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq);
```

这里的引用不是 sys table side row,但仍然是 DDL proof obligation:

```text
table column default 里有可执行表达式
+ 表达式引用一个独立 sequence 对象
+ create/alter 时会校验 sequence 存在
+ drop/rename sequence 后这个表达式还会在未来 INSERT 中执行
= sequence DDL 必须 rewrite 或 block dependent defaults
```

新增探针:

```text
/Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

结果:

```text
SUMMARY total=5 findings=3 skipped=0
```

红格:
- `DROP SEQUENCE seq` 成功,但 `t.a DEFAULT NEXT VALUE FOR seq` 仍留在 `SHOW CREATE TABLE`,后续 default insert 报 `1146 Table '<db>.seq' doesn't exist`。
- `RENAME TABLE seq TO seq2` 成功,但 table default 没 rewrite 到 `seq2`,后续 default insert 报 `1146`。
- 跨库场景里 `tabdb.t` 默认值引用 `seqdb.seq`;`DROP DATABASE seqdb` 成功后,`tabdb.t` 留下坏 default。

绿格:
- sequence 存活时 default insert 正常消费 sequence。
- `CHANGE COLUMN a aa INT DEFAULT NEXT VALUE FOR seq` 在 sequence 存活时保持 default 可用,说明不是 default 表达式整体坏,而是 sequence object DDL 缺少反向依赖处理。

为什么这次能 work:
- 它复用了 CHECK/partial-index 那轮的"表达式 owner"经验,但把被引用对象从 column 扩成独立 DDL object。
- 它也复用了 table-cache 的"remove broader object 后引用必须 cleanup/block"经验,但 oracle 更干净:未来 default insert 直接报错,不是异步 GC 或运行时策略。
- AI 的关键动作是把源码里的 `getSequenceDefaultValue` / runtime `GetSequenceByName` 连起来:默认值保存的是可重放 SQL 文本,执行时按名字找 sequence,而 `DROP SEQUENCE` / sequence rename / `DROP DATABASE` 没有 reverse dependency scan。

新的 selector:

```text
executable schema expression references separate DDL object
+ create/alter validates target
+ remove/rename path lacks reverse dependency scan
= dangling schema expression after DDL
```

暂停门:
- 草案:`/Users/bba/pc/ai-native-sequence-default-reference-draft.md`。
- 方法 case:`/Users/bba/pc/ai-native-id30005-method-case.md`。
- 不继续扩更多 sequence 变体。下一步应先讨论修复语义:`DROP SEQUENCE` block,sequence rename block/rewrite,`DROP DATABASE` 在删除被外部 table default 引用的 sequence 时 block。

### 9.9 affinity owner negative screen:外部状态不等于独立 SQL owner
sequence-default 是正例之后,下一轮源码扫描又看到一个容易误判的 owner:affinity。它看起来有 PD-side group state,group ID 又包含 table/partition ID,很像 table-cache/stats 这种 side metadata。但源码拆开后,它不是同一种风险:

```text
SHOW AFFINITY 的行来自 live InfoSchema 中带 Affinity 的表/分区
+ PD group state 只是补状态列
+ table/partition group ID 由当前 tableID/partitionID 派生
+ drop/truncate/drop database 有 cleanup,危险 partition DDL 有 block
= 外部状态味道很强,但还不是优先 bug target
```

新增探针:

```text
/Users/bba/pc/ai_native_ddl_affinity_reference_probe.py
```

结果:

```text
SUMMARY total=6 findings=0 skipped=0
```

覆盖的 6 格:
- table affinity 的 `SHOW CREATE TABLE` / `SHOW AFFINITY` 可见性控制。
- `RENAME TABLE` 后 visible affinity 跟随新表名。
- `TRUNCATE TABLE` 后保留一条 visible affinity。
- `DROP TABLE` 后 visible affinity 消失。
- partition affinity 下 `TRUNCATE PARTITION` 保留分区 affinity,`DROP PARTITION` 和 `REMOVE PARTITIONING` 被 affinity owner block。
- `DROP DATABASE` 后 table/partition visible affinity 都消失。

为什么这个负例重要:
- 它把 selector 从"看到外部状态/ID-keyed 就跑"推进到"先确认 SQL-visible surface 是不是独立 side store"。
- stats/table-cache 的红格是 side metadata 本身暴露 stale object identity;affinity 的 `SHOW AFFINITY` 先从 live InfoSchema 枚举对象,PD state 缺失时也只是状态列为 NULL。
- 这能减少 AI 的假高信号扩张:PD group 这种外部执行状态要有 deterministic stale-public-state oracle,否则不应打成下一轮 live matrix。

新的降权规则:

```text
external side state
+ public SQL rows are enumerated from live InfoSchema
+ existing DDL already has cleanup/block coverage
= negative selector, unless a separate stale public state surface is proven
```

### 9.10 functional index hidden column:multi-schema 差异不必然是红格
回到 column/index 主线后,一个自然候选是 functional index。它是 index object,但表达式依赖通过 hidden generated column 实现:

```sql
CREATE TABLE t(a INT, b INT, INDEX idx_expr ((a + 1)));
```

证明义务看起来是:

```text
functional index expression 引用 column a
+ ALTER 改名/删除/改类型 column a
= 必须 block,除非 functional index owner 已先被删除
```

新增探针:

```text
/Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py
```

结果:

```text
SUMMARY total=5 findings=0 skipped=0
```

覆盖的 5 格:
- `SHOW CREATE TABLE` 能看到 functional index expression。
- `RENAME/CHANGE/MODIFY/DROP COLUMN a` 都以 `3837 expression index dependency` block,且 schema 保持原样。
- 顺序执行 `DROP INDEX idx_expr; RENAME COLUMN a TO aa` 成功,说明 drop-index owner 会清理 hidden generated column dependency。
- 单条 multi-schema `DROP INDEX idx_expr, RENAME COLUMN a TO aa` 两种顺序都 block。
- 单条 multi-schema `DROP INDEX idx_expr, DROP COLUMN a` 两种顺序也都 block。

这个结果的价值不是发现 bug,而是补了一个边界:

```text
sequential DDL removes owner then changes referenced object
+ single multi-schema statement still validates against original dependency graph
= 不自动判红
```

也就是说,以后看到"顺序执行可以,同一条 multi-schema block"时,不能直接归类为路径不一致 bug。只有当产品语义明确支持 statement 内依赖消解,或者另一个 owner 在同类场景已经支持这种消解时,才把它提升为红格。否则它只是一个 conservative block,而且这里错误族 `3837` 正确、schema preservation 也正确。

### 9.11 DB-level placement:覆盖充分的 owner 要快速降权
functional index 之后,placement policy 还有一个没单独跑过的 owner 层:database default placement。它容易被误判成新方向,因为同一个 policy 可以被 DB、table、partition 三层引用,而且新表会继承 DB default:

```text
DB.PlacementPolicyRef
+ TableInfo.PlacementPolicyRef
+ PartitionDefinition.PlacementPolicyRef
= DROP PLACEMENT POLICY 必须扫描全部引用层
```

源码先给了强信号:
- `pkg/ddl/schema.go:120` 的 `onModifySchemaDefaultPlacement` 会 rewrite/clear DB-level `PlacementPolicyRef`。
- `pkg/ddl/executor.go:284` 在 `CREATE DATABASE` 时保存 DB placement ref。
- `pkg/ddl/create_table.go:842` 让新表继承 DB default placement。
- `pkg/ddl/placement_policy.go:373` 和 `:450` 的 InfoSchema/Meta 两条 in-use 检查路径都会先扫 DB ref,再扫 table/partition ref。
- `pkg/ddl/placement_policy_ddl_test.go:94-150` 已经把 DB/table/partition refs 放在同一个 in-use 单测里。

新增探针:

```text
/Users/bba/pc/ai_native_ddl_db_placement_reference_probe.py
```

结果:

```text
SUMMARY total=6 findings=0 skipped=0
```

覆盖的 6 格:
- `SHOW CREATE DATABASE` 能看到 DB placement policy。
- `DROP PLACEMENT POLICY` 在 DB ref 存在时以 `8241` block,且 DB ref 保持可见。
- `ALTER DATABASE ... PLACEMENT POLICY pp2` 后旧 policy 可 drop,新 policy 仍 in-use。
- `ALTER DATABASE ... PLACEMENT POLICY DEFAULT` 会释放 DB ref。
- `DROP DATABASE` 会释放 DB-level policy ref。
- `ALTER DATABASE` 前创建的旧表保留旧 policy,之后创建的新表继承新 DB default;两边 policy 都被 in-use 保护。

这个 negative case 的方法论价值是:

```text
引用层级多
+ 源码已有统一 in-use scan
+ 现有测试已经覆盖 DB/table/partition
= 只需要小矩阵校准,不要继续扩普通 placement fuzz
```

也要注意继承语义:DB default 改变并不要求 rewrite 已存在的 table ref。旧表保留旧 policy、新表继承新 policy 是边界控制,不是路径不一致。

### 9.12 view reference:创建时校验不等于 DDL owner
sequence-default 命中之后,一个自然候选是 view:

```sql
CREATE VIEW v AS SELECT a, b FROM t WHERE a > 0;
```

它很像 schema object 引用 base table/column,而且 `CREATE VIEW` 确实会先 preprocess 校验 SELECT:
- `pkg/executor/ddl.go:328` 对 view SELECT 做 preprocess。
- `pkg/ddl/executor.go:1507` 构造 `TableInfo.View`。
- `pkg/ddl/create_table.go:1757` 把 SELECT restore 成字符串。
- `pkg/meta/model/table.go:780` 的 `ViewInfo.SelectStmt` 就是字符串字段。
- `pkg/executor/show.go:1671` 的 `SHOW CREATE VIEW` 再把这个字符串吐出来。

新增探针:

```text
/Users/bba/pc/ai_native_ddl_view_reference_probe.py
```

结果:

```text
SUMMARY total=5 findings=0 skipped=0
```

覆盖的 5 格:
- live view 控制:能查询 base table,`SHOW CREATE VIEW` 暴露 SELECT text。
- base table rename 后 DDL 成功,view 保留旧表名并 invalid。
- base column rename 后 DDL 成功,view 保留旧列名并 invalid。
- base table drop 后 DDL 成功,view 保留旧 SELECT text 并 invalid。
- 跨库 base database drop 后,外部 view 存活,保留旧 cross-DB 名字并 invalid。

这个 negative case 修正了一个 selector 误区:

```text
create-time validation
!= DDL-time maintained dependency
```

view 是名字绑定 SQL text,不是 object-identity owner。它和 sequence-default 的差别是:sequence default 是 live table default 的执行语义,引用 missing sequence 后普通 insert 直接坏掉,且默认表达式属于表 schema 的一部分;view 的 invalidation 是按名字延迟解析的对象语义。以后遇到 `CREATE ... AS SELECT ...`、binding SQL、advisor recommendation、历史 job 记录这类 SQL 文本,要先证明产品语义要求跟随 object identity,否则不要放进 rewrite/block 矩阵。

### 9.13 resource group SWITCH_GROUP:字段像引用,但没有存在性承诺
view 之后又筛了一个容易误判的候选:

```sql
CREATE RESOURCE GROUP src
  RU_PER_SEC = 100
  QUERY_LIMIT=(EXEC_ELAPSED='1s' ACTION=SWITCH_GROUP(target));
```

它看起来像 resource group object 引用另一个 resource group object。源码也确实有一个名字字段:
- `pkg/meta/model/resource_group.go:33`/`:34` 存 `Action` 和 `SwitchGroupName`。
- `pkg/meta/model/resource_group.go:127` 会在 public surface 打印 `ACTION=SWITCH_GROUP(name)`。
- `pkg/ddl/resource_group.go:159` 还有 `check the resource group not in use` 的 TODO。

但真正决定它是否进入 rewrite/block 矩阵的是 create/alter 的语义:
- `pkg/ddl/resourcegroup/group.go:56` 只检查 `SWITCH_GROUP` 名字非空。
- `pkg/ddl/resourcegroup/group.go:59` 明确留下 TODO:validate the switch group name to ensure it exists。
- `pkg/ddl/resource_group.go:342` 的 validation 只是调用同一个转换路径。

新增探针:

```text
/Users/bba/pc/ai_native_ddl_resource_group_reference_probe.py
```

结果:

```text
SUMMARY total=3 findings=0 skipped=0
```

覆盖的 3 格:
- missing switch target:创建成功,`information_schema.resource_groups` 原样显示 missing name。
- drop switch target:source 继续显示旧 target 名字,但这和 missing target 允许行为一致。
- `ALTER RESOURCE GROUP ... QUERY_LIMIT=NULL`:能清掉 stored switch-group name。

这个 negative case 把 selector 再推进一步:

```text
field stores another object's name
+ public surface shows that name
- create/alter does not validate target existence
= not yet an object-identity reference owner
```

sequence-default 和它的差别非常关键:sequence default 的 create/alter 会校验 sequence 存在,所以 drop/rename/drop-database 不反扫会留下违反承诺的 schema expression;`SWITCH_GROUP` 当前没有这个存在性承诺,只能记为未校验名字参数或未实现 validation,不应拿来证明 DDL reference bug。

### 9.14 hypo index:session-local side metadata 也要 DDL invalidate
resource-group 之后,搜索回到 index owner。columnar/vector index 有独立 `ActionAddColumnarIndex`,但当前 testbed 没有 TiFlash,外部 failpoint 也不可用,不适合强行打。沿同一条 index-kind 线,更轻的目标是 hypo index:

```sql
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SHOW CREATE TABLE t;
```

源码形态:
- `pkg/ddl/executor.go:5121` 对 `USING HYPO` 先构造 `IndexInfo`,也就是已经做过表/列校验。
- `pkg/ddl/executor.go:5043` 把它存进 `SessionVars.HypoIndexes[schema][table][index]`,不进真实 `TableInfo`。
- `pkg/executor/show.go:1207` 会按当前 schema/table name 把 session-local hypo index 合进 `SHOW CREATE TABLE`。
- `pkg/executor/show.go:1277` 打印 `/* HYPO INDEX */`。

新增探针:

```text
/Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py
```

结果:

```text
SUMMARY total=7 findings=6 skipped=0
```

红格:
- `RENAME COLUMN a TO aa` 后,`SHOW CREATE TABLE` 仍显示 `KEY idx_a (a) /* HYPO INDEX */`。
- `CHANGE COLUMN a aa INT` 同样 stale。
- `DROP COLUMN a` 后,`SHOW CREATE TABLE` 仍显示 dropped column 上的 key。
- `DROP TABLE t; CREATE TABLE t(...)` 后,旧 hypo index 贴到新表上。
- `RENAME TABLE t TO t2; CREATE TABLE t(...)` 后,新建的旧名表 `t` 获得旧 hypo index。
- `DROP DATABASE db; CREATE DATABASE db; CREATE TABLE t(...)` 后,旧 hypo index 贴到重建 schema/table 上。

其中 column rename 的 `SHOW CREATE TABLE` 已经不可重放:

```text
ERROR 1072 (42000): column does not exist: a
```

这个正例说明 selector 还要覆盖 session/cache 层:

```text
session-local side metadata
+ created by DDL syntax after validating object names
+ merged into public DDL/API output
+ keyed by schema/table/column names
= high-value DDL invalidation/rekey target
```

它也反过来解释了 resource-group 负样本为什么不能算红:resource-group 没有 target-existence 承诺;hypo index 有列存在性承诺,而且会被 `SHOW CREATE` 当成表定义的一部分输出。

修复语义也反过来强化了 selector:
- 不能只在 `SHOW CREATE TABLE` 过滤 stale hypo index,因为根因是 `SessionVars.HypoIndexes` 里的旧 schema/table/column 键没有随 DDL cleanup/rekey;只过滤展示面会留下同名对象重建后的 resurrection。
- column rename/change/drop 更适合 drop 受影响的 hypo index,而不是 block DDL;hypo index 是 advisory session metadata,阻止真实表结构变化太重。若 owner 想保留体验,rename/change 可做列名 rewrite,但要覆盖表达式/partial condition 等复杂元数据。
- table/database drop 必须清 session map;table rename 可以 rekey 或 drop,多表 rename 若 rekey 要处理 swap/cycle。
- `SHOW CREATE TABLE` 仍应加防御式校验:session-local index 的列名/offset 必须能被当前 `TableInfo` 证明仍有效,否则不要合进输出。这是兜底,不是主修复。

下一轮目标卡:

```text
DDL syntax creates/mutates auxiliary metadata
+ stored in SessionVars / cache / side table
+ keyed by old names or IDs
+ later merged into SHOW / information_schema / DDL-like output
+ no obvious DDL cleanup/rekey helper on column/table/container paths
= build a 3-7 cell invalidation matrix
```

这张卡的目的不是继续挖 hypo-index 变体,而是验证 selector 是否能提前预测下一个 owner:要么再命中一个 stale-reference bug,要么找到一个明确 helper 并把它写成新的负样本规则。

id30006 后的第一轮 follow-up 把 selector 又收紧了一步:

```text
Reject public surfaces that are intentionally historical/user-policy text.
Only build the DDL invalidation matrix when the surface claims current schema state.
```

三个筛选样本:
- `HypoTiFlashReplicas` 也是 session map,但只在 `EXPLAIN`/planner 路径里使用,没有并入 `SHOW CREATE TABLE` 或 DDL-like schema output,所以不是当前 DDL metadata target。
- SQL binding 会保存并展示带 `USE INDEX` 的 `BindSQL`;`CREATE BINDING` 也会通过内部 `EXPLAIN` 校验。但现有测试已经期望 `DROP INDEX` 后 `SHOW GLOBAL BINDINGS` 仍保留一行,说明这是 saved policy SQL text,不是 DDL owner 必须 rewrite/block 的 object ref。
- local temporary table 的 session metadata 是显式设计边界:`SessionExtendedInfoSchema` 注释说明 local temp table 与 database 是 loose relationship,`DROP DATABASE` 后仍能通过 session 找到不是 cleanup miss。

stateful 探针当前状态:
- 已落地 14 个 stateful 格子,覆盖 `reorgPartRollback2/3/4` × 两条 DDL:普通表 `PARTITION BY ... UPDATE INDEXES` rollback,以及 partitioned table `REMOVE PARTITIONING` rollback;另覆盖 `reorgPartFail4/5` 一次性失败重试、`truncatePartCancel1` 和 `truncatePartFail1/2/3`。
- oracle 仍保持 DDL-only 边界:rollback 后检查原始 partition/global marker 是否恢复、临时新增 partition policy 是否解除、原有 table/partition policy 是否仍 in-use、`ADMIN CHECK` 与 index-vs-table rowset 是否一致。
- 已重建 failpoint-enabled TiDB owner 后跑通:`SUMMARY total=14 findings=0 skipped=0`。
- 六个 rollback 格子均绿:普通表 `PARTITION BY ... UPDATE INDEXES` rollback 后回到非分区表、释放新增 partition policy、保留 table policy;partitioned table `REMOVE PARTITIONING` rollback 后恢复 partition metadata、table/partition policy refs、global marker 和行集一致性。
- 四个 reorg retry 格子均绿:`reorgPartFail4/5` 一次性失败后 retry 成功,`PARTITION BY ... UPDATE INDEXES` 保留 partition/global/local/policy refs,`REMOVE PARTITIONING` 释放 partition policy、保留 table policy、清除 global marker。
- truncate stateful 格子也绿:`truncatePartCancel1` 保留原始 partition/policy/global/rowset;`truncatePartFail1/2/3` 一次性失败后 retry 成功,只删除被 truncate partition 的行,policy refs/global marker 保持正确。
- 这个结果继续降权 partition reorg rollback、reorg retry 和 truncate transient failure 的显式 failpoint 层;下一轮应转向 delete-range/old global-index cleanup 的消费/GC 侧窗口,以及 placement bundle rebuild 失败。

delete-range metadata 探针当前状态:
- 新增 `/Users/bba/pc/ai_native_ddl_delete_range_probe.py`,把 `mysql.gc_delete_range` 作为 DDL metadata oracle,区分 table/partition range 与 global-index range。
- 已跑通 2 格:`SUMMARY total=2 findings=0 skipped=0`。
- `REMOVE PARTITIONING` from partitioned table with global index 会登记 1 条旧 global-index range 和 4 条 table/partition range。
- `DROP GLOBAL INDEX` on partitioned table 只登记 1 条 logical index range,没有误登记 table/partition range。
- 普通 delete-range 入队层也降权;下一步若继续这条线,应打 GC worker 消费失败/redo 或更深的 placement bundle failure,而不是继续看 SQL 行集。

placement-bundle failure 探针当前状态:
- 新增 `/Users/bba/pc/ai_native_ddl_placement_bundle_failure_probe.py`,用 `pkg/domain/infosync/putRuleBundlesError` 外部注入 PD bundle 通知失败。
- 已跑通 5 格:`SUMMARY total=5 findings=0 skipped=0`。
- persistent failure 绿:`ALTER TABLE ... PLACEMENT POLICY`、`ALTER TABLE ... PARTITION ... PLACEMENT POLICY`、`ALTER PLACEMENT POLICY` 失败后 metadata/reference 都没有被污染。
- one-shot retryable failure 绿:`ALTER TABLE ... PLACEMENT POLICY` 和 `ALTER PLACEMENT POLICY` retry 成功,dependency 仍被 `DROP PLACEMENT POLICY` 保护。
- 这说明 `putRuleBundlesError` 这层已有测试覆盖的 owner 原子性比较稳;下一轮不应继续堆 placement bundle 失败变体,除非发现新的 multi-owner DDL 路径。

FK table/index object 探针当前状态:
- 新增 `/Users/bba/pc/ai_native_ddl_fk_object_reference_probe.py`,把 FK 从 column owner 扩到 table/index object owner。
- 已跑通 10 格:`SUMMARY total=10 findings=0 skipped=0`。
- table rename 绿:parent rename、child rename、同一条 `RENAME TABLE` 里 child-then-parent / parent-then-child 两种顺序都能正确 rewrite/preserve FK target,行为 oracle 证明 FK enforcement 仍在。
- table/index block 绿:drop parent table、truncate parent table、drop parent supporting index、drop child supporting index 都被 FK owner 阻止,且失败后原 FK 仍可用。
- index object rewrite/allow 绿:rename supporting index 不影响 FK;drop redundant supporting index 在另一个 covering index 存在时允许。
- 这次最有方法论价值的是"源码怀疑红格 -> 外部行为 oracle 证绿":多表 rename 顺序看起来危险,但 helper 机制兜住了。基础 FK table/index owner 因此降权,后续除非有新状态维度,不要继续堆基础 FK case。

### 9.15 Reorganize partition:green owner 也要看 sibling path
id30006 后的 follow-up 没有继续扩大 session side-metadata。columnar/vector index 的源码信号一度看起来可疑,但当前 TiFlash-less 环境无法创建有效对象,只能记为"source-high-signal, needs TiFlash/testkit environment",不能强行当产品结果。真正命中的下一格来自另一个问题:

```text
一个 owner 的常规路径已经 green 之后,是否还有 sibling DDL path 走了完全不同的 prepare/iterate/finalize?
```

global index 在前面的 object-reference/stateful 矩阵里已经有一批绿格:
- `DROP PARTITION` / `TRUNCATE PARTITION` 后 `ADMIN CHECK` 与 rowset 一致。
- `REMOVE PARTITIONING` 能清掉 global marker,保留行集。
- `PARTITION BY ... UPDATE INDEXES` rollback/retry 后 global/local marker 和 rowset 都一致。

如果只看 owner 结论,global index 应该降权。但 `REORGANIZE PARTITION` 的实现形态不同:它不是简单删除/改写,而是"从 dropping partitions 拷贝到 adding partitions,再给 replacement global indexes 回填 non-touched partitions"。源码注释本身给出了证明义务:

```text
replacement global index
= adding partitions 中的行
+ every still-live non-touched partition 中的行
```

新增探针:

```text
/Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py
```

结果:

```text
SUMMARY total=2 findings=1 skipped=0
```

最小红格:

```sql
CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL)
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
);
INSERT INTO t VALUES (12,120),(30,300);

ALTER TABLE t REORGANIZE PARTITION p1 INTO (
  PARTITION p1a VALUES LESS THAN (15),
  PARTITION p1b VALUES LESS THAN (20)
);

SELECT GROUP_CONCAT(CONCAT(a,':',b) ORDER BY b)
FROM t USE INDEX(idx_b) WHERE b >= 0;    -- 12:120

SELECT GROUP_CONCAT(CONCAT(a,':',b) ORDER BY b)
FROM t IGNORE INDEX(idx_b) WHERE b >= 0; -- 12:120,30:300

ADMIN CHECK TABLE t; -- ERROR 8223: missing index entry
```

源码模型:
- `pkg/ddl/partition.go:4048-4052` 的流程要求更新 non-touched partitions 上的 replacement global indexes。
- `pkg/ddl/partition.go:4136-4153` 先找第一个 non-touched partition,设置 `reorgInfo.PhysicalTableID`。
- `pkg/ddl/index.go:3524-3533` 的 `getNextPartitionInfo` 在当前 partition 属于 `AddingDefinitions` 时会优先继续 adding-definitions iterator。
- `pkg/ddl/index.go:3621-3637` 的 `findNextNonTouchedPartitionID` 只跳过 `DroppingDefinitions`,没有跳过 `AddingDefinitions`。
- 结果是 non-touched 阶段可能从前置 non-touched partition 进入 adding definitions,adding iterator 结束后 job 认为 replacement global index backfill 完成,遗漏后置 non-touched partition。

这次真正新增的方法论不是"global index 还有 bug",而是下面这张 selector:

```text
same owner has green coverage on common DDL paths
+ sibling DDL path has different multi-stage iterator
+ source/comment says all remaining refs/objects must be visited
+ 2-row before/after-range oracle exists
= high-density target
```

这个 selector 很适合 AI:它要求读懂已有绿格、源码流程差异和注释里的全量访问义务,然后把反例压缩成两行数据。普通随机 fuzz 很难知道要把一行放在被 reorg 的 range 内,另一行放在后置 non-touched partition 里;AI 通过 iterator 模型可以直接生成这个形状。

暂停规则也要更新:命中 id30007 后不要继续堆 `REORGANIZE PARTITION` 变体。先确认修复语义和证明义务,再决定是否扩到 hash/list partition、多个 global index、local/global 混合等 blast radius。否则会从"验证方法"滑回"枚举更多 case"。

### 9.16 Testbed/capability gate:环境差异也是方法论信号
2026-07-02 切到用户给的 testbed `8192975` 后,同一个 id30007 探针仍然稳定复现:

```text
testbed: 8192975
namespace: testbed-tps-8192975-1-14
SQL: 127.0.0.1:14000 -> pod/fp-tidb:4000
status/failpoint: 127.0.0.1:18080 -> pod/fp-tidb:10080
SELECT VERSION(): 8.0.11-TiDB-v8.4.0-this-is-a-placeholder
probe: SUMMARY total=2 findings=1 skipped=0
```

2026-07-09 在新的 QA testbed `8220955` 上重新跑同一最小红格,当前 master 形态
`8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948` 仍稳定复现:

```text
REORGANIZE PARTITION p1 -> p1a/p1b 成功
USE INDEX(idx_b)   WHERE b >= 0 => 12:120
IGNORE INDEX(idx_b) WHERE b >= 0 => 12:120,30:300
ADMIN CHECK TABLE t => ERROR 8223 missing index entry for handle 2 / b=300
cleanup: DROP DATABASE qa_gidx_8220955 succeeded
```

这不是新 root-cause 计数,而是一个高质量 QA selector 的再确认:同一两行数据能同时触发
用户可见 wrong-result(`USE INDEX` 漏后置 non-touched partition 行)和存储一致性 oracle
(`ADMIN CHECK TABLE` 8223)。后续找“数据/索引不一致”优先选这种源码里存在全量访问义务、
且有 `index arm` vs `table arm` + `ADMIN CHECK` 双 oracle 的格子,不要退回宽泛随机 fuzz。

这个结果改进了两点方法论。

第一,**跨环境复现是分类器**。同一个两格探针在旧版本形态 testbed 上也命中,说明 id30007 更像长期潜伏的 DDL iterator 缺陷,而不是某个最新 master 改动的偶发回归。以后新命中进入暂停门时,如果低成本可做,应补一轮不同 build/testbed 上的同探针复现,用来区分:
- master-only 回归;
- feature/capability 缺失导致的 skip;
- 长期存在的结构性缺陷。

第二,**探针要有 capability gate,不能把环境差异误报成产品结论**。这次环境里 `@@tidb_version` 不存在,但 `SELECT VERSION()` 和 `/status` 可用;NodePort SQL 直连会卡住,但 port-forward 到 `fp-tidb` 正常。以后 harness 应先记录:
- SQL/status/failpoint 入口是否可用;
- managed TiDB 是否为 0,`fp-tidb` 是否唯一 DDL owner;
- 目标 DDL 语法是否可创建最小对象;
- 缺功能时输出 `SKIP(capability)` 而不是把语法/环境错误写成 bug。

这条规则和 DDL-only 边界一致:环境检查不是为了扩大搜索,而是为了降低 triage 噪声,让"新 bug 命中"能更快变成可信的方法论证据。

delete-range GC worker 消费侧当前判断:
- `ignoreDeleteRangeFailed` 不是制造失败的 failpoint,而是把底层 delete-range 错误吞掉。
- `tidb_gc_run_interval` 最小仍为 `10m0s`,尝试设为 `1s` 会被截回 10 分钟。
- 当前没找到低成本 SQL/HTTP 入口直接触发 `deleteRanges` / `redoDeleteRanges`;因此这条线暂时不符合"高效发现 bug"的执行约束。若后续要打,更适合写 Go/in-process harness 或找到已有内部 API。

### 9.17 Table-lock owner-key rewrite:session side state 也能是 DDL owner
id30008 把 selector 从 sys-table side metadata 和 sibling iterator 继续推进到 **DDL-created session side state**:

```text
side state key = owner/container key + object ID
DDL move/rekey path preserves object ID but changes owner/container key
cleanup path trusts the old owner/container key
= high-density stale-cleanup target
```

最小红格:

```sql
CREATE DATABASE ai_lock_src;
CREATE DATABASE ai_lock_dst;
CREATE TABLE ai_lock_src.t (a INT);

-- session 1
LOCK TABLES ai_lock_src.t WRITE;
RENAME TABLE ai_lock_src.t TO ai_lock_dst.t;
UNLOCK TABLES;

-- session 2
INSERT INTO ai_lock_dst.t VALUES (1);
```

在 `enable-table-lock=true` 的本地 DDL harness 中,`RENAME TABLE` 和 `UNLOCK TABLES` 都成功,但 session 2 的 `INSERT` 报:

```text
[schema:8020] Table 't' was locked in WRITE by server: ..._session: 1
```

相邻绿格:

```text
go test -tags=intest ./pkg/ddl -run TestRenameTableWithLocked -count=1
=> PASS
```

这说明不是 table-lock + rename 整体坏,而是跨库 rename 改变 `SchemaID` 后 session lock entry 没同步。源码链路很窄:

- `session.lockedTables` 以 `TableID` 存 map,但 value 里保存 `SchemaID`。
- cross-schema `RENAME TABLE` 保持 `TableID`,把同一个 `TableInfo` 从 old schema 移到 new schema。
- `UNLOCK TABLES` 从 session map 构造 unlock job,继续使用 old `SchemaID`。
- unlock worker 用 `(old SchemaID, TableID)` 找表,找不到就按"table maybe dropped"跳过,最后 session map 被清空,但 new schema 下的 `TableInfo.Lock` 仍在。

强 oracle 是 **cleanup 后真实行为**:

```text
UNLOCK TABLES succeeded
=> another session should be able to INSERT/SELECT
```

这个 case 的方法价值:

- 它证明 session/local metadata 也可以进入 DDL owner lane,前提是该状态由 DDL syntax 创建,并且后续 DDL cleanup 对用户行为有强 oracle。
- 它纠正了一个过度降权规则:不能因为状态是 session-local 就一律排除;要看它是否会被 DDL move/rekey path 留成跨 session 可见的 live object lock。
- 它给下一轮加了一个新问题:对象 ID 不变但容器 key 变了时,所有 side state 是否同时 rewrite 了 owner/container key?

暂停门:

- 草案:`/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md`。
- 方法 case:`/Users/bba/pc/ai-native-id30008-method-case.md`。
- `found_bug id30008` 已入库,并在用户给的 testbed `8192975` 上确认复现。该 testbed 初始 `enable-table-lock=false`,按要求更新 `tc.spec.tidb.config` 并重启 `fp-tidb` 后,`SHOW CONFIG` 变为 `true`;随后 session1 `LOCK/RENAME/UNLOCK` 返回 0,session2 `INSERT` 报 `8020`,清理成功。
- 不继续扩 multi-table rename、read lock、close-session cleanup、drop database 等 table-lock 变体,先确认修复语义:cross-schema rename 同步 session lock `SchemaID`,或 unlock 按 `TableID` 反查当前 schema。

## 10. 本轮验证产出(方法论有效性的证据,存于 bug 库 found_bug 表)
- **found_bug id1**(中危·数据完整性):`CHANGE/MODIFY COLUMN` 改名 CHECK 约束引用列 → 静默删约束、违反数据被接受。根因:`modify_column.go` 漏调 `ErrDependentByCheckConstraint(3959)`。
- **found_bug id2**(低危·错误处理):`RENAME COLUMN` 改名 partial index 谓词列 → 误导性 `1054` 而非 `8272`。根因:`RenameColumn` 漏调 `checkColumnReferencedByPartialCondition`。
- **found_bug id30001**(高危·wrong-result):partial index 可用性判断把不蕴含 partial 条件的查询当作可用,导致 `SELECT` 静默漏行。最小形态:`INDEX pi(b) WHERE a < 3` + `WHERE a >= 0` 使用 `pi` 后只返回 partial subset。根因锚点:`DataSource.CheckPartialIndexes` → `partidx.CheckConstraints` 的 range 蕴含证明不安全。
- **candidate id30002**(wrong-result):predicate simplification 在 collation/coercibility 混合的 `IN`/`!=` 合并中删除必要谓词,导致 `WHERE` 行集大于 CASE-wrapped reference。
- **candidate id30003**(DDL side metadata):`RENAME/CHANGE COLUMN` 后 live schema 已更新,但 `SHOW STATS_HISTOGRAMS` 仍显示旧列名直到重新 `ANALYZE TABLE`。
- **candidate id30004**(DDL side metadata):cached table 所在 schema 执行 `DROP DATABASE` 后 table 消失,但 `mysql.table_cache_meta` 残留 dropped table ID。
- **candidate id30005**(DDL dangling reference):table column default 引用 sequence 时,`DROP SEQUENCE`、sequence rename、跨库 `DROP DATABASE` 可以留下指向 missing sequence 的 live default。
- **candidate id30006**(DDL side metadata):`USING HYPO` session-local index 通过 DDL 语法创建并并入 `SHOW CREATE TABLE`,但 column/table/database DDL 后没有 cleanup/rekey,导致 stale hypo index 贴回当前 schema surface。
- **candidate id30007**(DDL global-index reference):`REORGANIZE PARTITION` 成功后 replacement global index 漏掉后置 non-touched partition 的行,`USE INDEX` 漏行且 `ADMIN CHECK TABLE` 报 `8223`。
- **found_bug id30008**(DDL stale cleanup):table-lock 后跨库 `RENAME TABLE` 保持 table ID 但改变 schema ID,`UNLOCK TABLES` 成功后新库表仍被旧 session 锁住,另一个 session 写入报 `8020`;已在 testbed `8192975` 开启 table lock 后复现。
- **found_bug id30032**(中危·schema integrity):`ALTER TABLE ... ADD COLUMN b INT DEFAULT 1 CHECK(b > 0)` 成功且无 warning,但没有发布 CHECK,后续 `b=0` 被接受。它不同于 id1 的 `CHANGE/MODIFY COLUMN` 丢已有 CHECK;这里是 ADD COLUMN 接受新 inline CHECK 后没有把 child constraint 义务交给 `ActionAddCheckConstraint` owner。
- id1/id2 同一根因模式(§3),由同一张列引用矩阵挖出;id30001 来自 §10.1 指出的 TiKV-only partial-index 正确性空白,用 USE/IGNORE INDEX 行集差分 oracle 命中;stats/table-cache/hypo-index 验证了 DDL side-metadata owner selector;sequence-default 验证了 executable schema expression 可以引用独立 DDL object;id30007 验证了"green owner 之后继续看 sibling path iterator"这个新 selector;id30008 验证了"object ID 不变但 owner/container key 变化时,DDL-created side state 必须 rewrite cleanup key"这个新 selector;affinity/region-split/privilege/functional-index/DB-level placement/view/resource-group `SWITCH_GROUP` 这些负样本用于提高 selector precision。

### 10.1 三步聚集性循环的增量验证(本轮新增,master 13282a8bd0 + 旧 v8.4 binary)
用本地 `tidb-server`(unistore)对模型标红/标绿的格子逐一行为验证:
- **bug#1 爆炸半径比初记录大**(同根因的更多变体,均静默丢约束):
  - 多列 CHECK `CHECK(c2>0 AND c3>0)` 改名其中一列 → **整条约束消失**,连未改的 `c2` 保护也丢(`meta=0`,违反 `c2` 的行被接受)。
  - inline 列级 `c3 int CHECK(c3>0)`、`ALGORITHM=COPY`、multi-schema(`CHANGE … , ADD COLUMN …`)→ 全部静默丢。
  - `ADMIN CHECK TABLE` 不报错(非索引不一致),进一步说明需**行为 oracle**(插违反数据)才能抓到。
  - bug#1 跨版本:**v8.4.0 旧 binary 同样复现** → 长期存在,非 v9.0 回归。
- **模型标绿格子逐一证伪为非 bug**(阴性结果,纯 fuzz 给不了):
  - FK 父列 `pc` 经 CHANGE/RENAME 改名 → 子表 FK 引用实测跟随 `pc→pcx→pcy`,enforce 保留(`1452`)。
  - TTL 列经 CHANGE/RENAME 改名 → `TTL=` 子句实测跟随 `t1→t1x→t1y`。
  - generated col 跨 STORED/VIRTUAL/多列,所有 ALTER 一致阻止(3108/3106)。
  - vector/columnar 索引列在 `idx.Columns` 内,被常规索引列处理覆盖(`isColumnarIndexColumn` 守卫 + `renameColumnTo`)。
- **已补覆盖 partial index 正确性 oracle**:partial index 需 `Store==TiKV`(`ingest/env.go:60` 硬卡,本地 unistore 建不了),转到真 TiKV 集群后命中 found_bug id30001。

### 10.2 partial-index 蕴含矩阵的新命中(真 TiKV 集群)
新增小探针 `/Users/bba/pc/ai_native_partial_index_probe.py`,只做一件事:同一查询在 `IGNORE INDEX(pi)`、`USE INDEX(pi)`、`FORCE INDEX(pi)` 之间行集必须一致,并对 partial 条件/查询谓词做小规模系统枚举。

关键命中:
```sql
CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, INDEX pi(b) WHERE a < 3);
INSERT INTO t VALUES (1,1,1),(2,2,2),(3,3,3),(4,10,4),(5,NULL,5);

SELECT id,a,b FROM t IGNORE INDEX(pi) WHERE a >= 0 ORDER BY b; -- 1,2,3,4
SELECT id,a,b FROM t USE INDEX(pi)    WHERE a >= 0 ORDER BY b; -- 1,2 (漏 3,4)
```

观察:
- `ADMIN CHECK TABLE` 通过,说明索引内容符合 partial 定义;bug 在 planner 对 partial index 可用性的判断。
- 无 hint 路径已稳定复现:默认 session、fresh pseudo stats、未 `ANALYZE TABLE` 时,`ORDER BY b LIMIT` 可让优化器主动选择 `pi(b)` 并漏行。`EXPLAIN FORMAT='brief'` 显示 `IndexLookUp` + `IndexFullScan(Build) ... index:pi(b) keep order:true, stats:pseudo`。
- 同类扩展:`a <= const` + 查询下界也会错;`a != const` + 包含该 const 的 range 也会错。`a > const`/`a >= const` 的对称方向暂未复现。

2026-06-30 复盘后按语义族扩展了一轮,不是盲目加字符串:
- 工具:`/Users/bba/pc/ai_native_partial_index_probe.py --skip-fixed --matrix`,结果 CSV:`/Users/bba/pc/ai_native_partial_index_semantic_matrix_20260630_184141.csv`。
- 搜索空间:15 个 partial 条件语义 × 20 个查询谓词语义 × 4 个 order/limit × `{USE, FORCE, no_hint}`;共 1200 个格子。
- 行集 mismatch = 280,全部集中在 5 个语义族:
  - `upper_bound/lower_overlap`:138
  - `upper_bound/wide_range`:72
  - `upper_bound/boundary_range`:52
  - `excluded_point/boundary_range`:12
  - `upper_bound/or_widening`:6
- 阴性格子同样重要:`lower_bound`(`a > c`/`a >= c`)、`point`、`NULL`、大多数 `IN`/OR 组合没有命中;第一轮语义矩阵里的 `no_hint` 查询实际没有打到目标 ordered-index fast path。
- 后续 no-hint/stats-pressure 小矩阵命中同根因的更强形态:不需要 `USE/FORCE INDEX`,默认优化器在 `ORDER BY b LIMIT 5` 下会主动走错误 partial index。之前语义矩阵没看到 no-hint mismatch,原因是查询用了 `ORDER BY b,id`;单列 `pi(b)` 无法完整满足这个排序,反而避开了要测试的 ordered-index fast path。

这说明 id30001 不是一个孤立 SQL,而是两个高信号反例家族:
1. **上界 partial 条件被向右扩张的查询谓词误认为安全**,例如 `partial a <= 10` + `query a >= 0`。
2. **excluded point 被连续区间查询吞进去**,例如 `partial a != 3` + `query a BETWEEN 0 AND 3`。

同时也给下一轮剪枝:先不要继续在 lower-bound/NULL/点集上烧算力,而要把上界区间、excluded point、少量 OR widening 做成参数化语义生成器。

no-hint 命中还补了一条方法论修正:为了让差分结果确定,不要机械追加更多排序列。`ORDER BY b,id` 提高了确定性,但也改变了优化器可用的 fast path;更好的做法是在生成数据时保证 `b` 唯一,然后使用 `ORDER BY b LIMIT`。这让 oracle 同时满足"结果稳定"和"仍能触发目标路径"。

方法论修正:
- 对 optimizer/partial-index 类 bug,不要只看 DDL/DML 一致性,必须加 **planner 可用性 oracle**:`USE/FORCE INDEX(partial)` 与 `IGNORE INDEX(partial)` 的行集差分。
- "分析喂 fuzz"的搜索维度应从 `{特性 × ALTER 路径}` 扩到 `{partial 条件形状 × 查询谓词形状 × hint/无 hint × stats 状态}`。命中不是随机 SQL 语法,而是 range 蕴含模型的反例生成。

### 10.3 id30001 为什么能 work,以及改善空间
这次能 work 的关键不是 partial index 本身,而是搜索对象从"功能点"升级成了"证明器":

| 环节 | 这次做对了什么 | 下一步改善 |
|---|---|---|
| 目标选择 | 找到 TiKV-only partial index 正确性空白,避开已饱和的 add-index 普通路径 | 建一个"证明器目录":`Check/Can/Need/Prune/Imply/Rewrite` 类函数优先 |
| 证明义务 | 把 planner 可用性翻译成 `query predicate => partial predicate` | 让 AI 从代码自动抽 `(前提, 结论, fast path 后果)` 三元组 |
| 反例生成 | 手工枚举 `<, <=, !=, BETWEEN, NULL` 等边界 | 从"条件字符串矩阵"升级为语义反例生成:区间集合、NULL 三值逻辑、OR/AND 分配律、collation |
| oracle | `IGNORE INDEX` vs `USE/FORCE INDEX` 行集差分,误报低 | 加 fast-path/no-fast-path 的通用差分框架:hint、session var、stats、plan cache、旧版本对照 |
| 执行 | 小脚本秒级确认,没有先投大 harness | 命中后再把同一反例族规模化,并保存阴性格子避免重复搜索 |
| 反馈 | 命中后暂停,把 oracle/边界/生成维度写回 | 每次命中都产出"下一轮搜索空间 diff",不是只产出 repro |

本轮 no-hint 扩展说明"下一轮搜索空间 diff"必须具体到查询形状:旧矩阵的 `ORDER BY b,id` 是一个假阴性制造器,因为它削弱了 `pi(b)` 的排序吸引力;新版生成规则改为"数据保证 `b` 唯一 + 查询只写 `ORDER BY b`"。这是 AI 生成 oracle 时要自动检查的一类副作用:为稳定性加的约束,不能把目标 fast path 关掉。

下一轮不要简单把 partial-index 条件枚举做大,而应按"证明器攻击"扩展:

```text
proof target:
  partial-index predicate implication
counterexample families:
  disjoint ranges / partial overlap / excluded point / NULL leakage / OR widening / collation boundary
fast-path toggles:
  USE/FORCE vs IGNORE index / no hint with stats pressure / plan cache parameter reuse
oracle:
  row-set equality + plan evidence + stable user-table triage
feedback:
  每个 hit/negative 都回写到证明器假设表,更新下一轮生成权重
```

### 10.4 无命中也要改进 oracle
partition-pruning 第一轮 proof-obligation 探针没有命中 bug,但暴露出一个方法论改进点:只比较两个优化路径是不够的。`static` 与 `dynamic` pruning 如果共享同一错误语义,行集仍会一致。

因此 partition 类 oracle 从一开始就升级为三方差分:

```text
unpartitioned reference table
vs static partition pruning
vs dynamic partition pruning
```

这说明"没有发现 bug"也能让方法变强,前提是每轮都问:当前 oracle 能否捕捉同源错误?如果不能,先补参考路径,再扩大枚举空间。

partition-pruning 扩展跑进一步给出一个负反馈:在同一 proof family 内,把手写谓词扩成边界派生谓词后仍 `findings=0`。这时下一步不应继续机械堆同质谓词,而要切换到新的证明义务族,例如 plan-cache parameter drift:

```text
第一次参数让证明成立
→ 缓存/复用 fast path
→ 第二次参数让证明不成立
→ cached path vs direct path 必须仍等价
```

这也是 AI-native 的节奏控制:命中时暂停提炼,无命中时也要判断是 oracle 弱、生成空间薄,还是目标家族 bug 密度低。只有定位清楚,下一轮算力才不会又回到浅 fuzz。

### 10.5 predicate-simplification/collation 新命中
plan-cache 和 partition-pruning 的扩展给了阴性反馈后,本轮切到另一个证明器:`predicate_simplification`。这次不再依赖 rule blacklist 作为 oracle,而是用同一 TiDB 内部的语义变形:

```text
WHERE P
vs
WHERE CASE WHEN P THEN 1 ELSE 0 END = 1
```

SQL 的 WHERE 只保留 TRUE,所以这两者在稳定用户表上必须返回同一行集。CASE 包裹的价值是让参考路径不容易被同一个谓词化简器改写掉。

新命中:
```sql
CREATE TABLE t(
  id INT PRIMARY KEY,
  s VARCHAR(8) COLLATE utf8mb4_general_ci
);
INSERT INTO t VALUES (1,'a'),(2,'A'),(3,'b');

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

投影求值也证明原始谓词对 `A` 是 false:

```text
id=1 s=a in_pred=1 ne_pred=1 both_pred=1
id=2 s=A in_pred=1 ne_pred=0 both_pred=0
id=3 s=b in_pred=0 ne_pred=1 both_pred=0
```

根因模型:
- `s IN ('a','A')` 在 `utf8mb4_general_ci` 下匹配 `a/A`。
- `s != _utf8mb4'A' COLLATE utf8mb4_bin` 应按 binary 比较过滤掉 `A`。
- 优化后计划里 pushed selection 只剩 `in(s,"a")`;由于列 collation 仍是 case-insensitive,`in(s,"a")` 又匹配了 `A`。
- 源码锚点是 `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go` 的 `updateInPredicate` / `mergeInAndNotEQLists`:缩小 IN list 后删除 `!=` 谓词,但没有像 equality contradiction 分支那样保护字符串 collation/coercibility。

为什么这次方法有效:
- 先从源码找"会删除谓词"的证明器,而不是枚举 SQL 语法。
- 把证明义务说清楚:`IN`/`!=` 可合并 => 删除 `!=` 仍保持三值逻辑、类型和 collation 语义。
- 用 CASE-wrapped reference 解决了 predicate-simplification 缺少显式开关的问题。
- 命中后立刻停,没有继续把 800 格小矩阵扩成大 fuzz;先记录根因、控制组和下一轮权重。

后续生成权重:
- 上调所有"删除一个谓词、保留另一个谓词"的规则,尤其是字符串、collation、coercibility、prefix index、binary/ci 混合。
- 下调本轮已阴性的纯 integer/NULL `scalar AND (OR branch)` 组合;600+ 小样本没有分叉。
- 不要把 opt-rule blacklist 当作唯一 safe path;这类 helper 可能从多个 planner 路径被调用,CASE oracle 更稳定。

### 10.6 S16:coarse P 不能证明 rich Q

id630011/id630012 把 DDL 主线重新拉回了一个很高效的形状:

```text
代码检查了 P_coarse
系统因此相信 Q_rich
于是跳过完整 target-state validator
```

具体到 FK `MODIFY COLUMN`:

```text
P_coarse: type/flen/decimal 没变
Q_rich:  修改后的列仍与已有 FK 完全兼容
```

但 CREATE/ADD FK 的真实 target-state 兼容性比这更强:

```text
type
unsigned flag
charset
collation
SET NULL action 下的 child nullability
```

这个差值直接给出小矩阵:

```text
nullability:
  direct NOT NULL child + ON DELETE/UPDATE SET NULL -> 1830 reject
  nullable child FK -> MODIFY NOT NULL -> ALTER succeeds
  parent DELETE/UPDATE -> 1048

signedness:
  direct parent INT / child INT UNSIGNED FK -> 3780 reject
  signed/signed FK -> MODIFY child INT UNSIGNED -> ALTER succeeds
  parent UPDATE 1 -> -1 ON UPDATE CASCADE -> 1264
  DROP FK then ADD same FK -> 3780 reject
```

同一轮的负样本也很关键:

```text
collation change looked suspicious, but later indexed-column collation validation blocked it.
primary-key NULL looked suspicious, but later PK/default checks preserved the NOT NULL invariant.
resource-group in-use drop looked suspicious from TODO, but DROP RESOURCE GROUP had a reverse
dependency guard.
```

方法论改进:

- 先把 `P_check` 和 `Q_claim` 分开写。不要把函数名里的 `newCol` 当成"完整目标态"。
- 列出所有 `Q_claim` 需要但 `P_check` 没覆盖的 D 维度。
- 对每个 D 维度先做 later-validator coverage pass:后面有没有统一的完整 target-state check?
- 只有没有 coverage 的 D 维度才进入小矩阵;有 coverage 的 D 维度作为负样本记账。
- 红格必须配强 oracle:direct target-state rejection、transition acceptance、behavior consequence、round-trip revalidation。这样能避免把"ALTER 成功"误判成 bug。

这解释了为什么这套方法比随机 FK fuzz 快:AI 不是枚举 type/action 组合,而是在源码里找"证明的粒度差",再把差值压成 3-5 个 SQL cell。

### 10.7 S17:raw writer 也有证明义务

id630013 把同一个框架从 schema validator 推进到 DDL data reorg writer:

```text
代码绕过了 normal DML safe path
系统因此相信 row invariant 仍成立
于是直接写入转换后的 row
```

具体到 `MODIFY COLUMN`:

```text
safe path:
  AddRecord/UpdateRecord -> CheckRowConstraint
  ADD CHECK -> verifyRemainRecordsForCheckConstraint

fast/reorg path:
  decode old row
  CastColumnValue(old -> new)
  EncodeRow
  txn.Set
```

这里的 P/Q 是:

```text
P_check: old row 满足 CHECK,且类型转换成功
Q_claim: converted row 仍满足 CHECK
D_dim:   有损但成功的转换会改变谓词真值,如 0.40 -> INT 0
```

小矩阵因此非常小:

```text
DECIMAL(10,2) 0.40 + CHECK(a > 0) -> MODIFY a INT -> final a=0,a>0=0
DOUBLE 0.4       + CHECK(a > 0) -> MODIFY a INT -> final a=0,a>0=0
VARCHAR '0.4'    + CHECK(a > 0) -> MODIFY a INT -> final a=0,a>0=0

ADD CHECK(a > 0) to INT table containing 0 -> 3819 reject
INSERT 0 into altered table -> 3819 reject
SHOW WARNINGS after ALTER -> empty
```

方法论改进:

- 不只审计 validator,也审计 writer。任何 DDL backfill/reorg 只要直接写 KV,都要问它有没有复用 DML 的 invariant check。
- row invariant 包括 CHECK、partition membership、generated/hidden column consistency、FK action 能否写入、TTL/masking 等依赖行值的规则。
- 种子生成不是枚举类型转换,而是构造 `old predicate = true` / `new predicate = false` / `conversion succeeds`。
- `ADMIN CHECK TABLE` 不是这个类的强 oracle;它能通过 id630013,所以必须直接投影 CHECK predicate,并用 ADD CHECK/DML 作为 safe-path reference。
- 命中后暂停:不要扩所有 numeric/string 类型。下次只在新的 raw writer 或新的 invariant owner 上重开。

这说明"证明义务"的边界要从"代码检查了什么"扩成"代码跳过了哪个安全路径"。有些 bug 不在缺少一个前置 validator,而在后端 writer 选择了 fast path 后没有补回 safe path 的副作用。

## 11. GitHub DDL held-out 反馈:DDL 不止 reference ownership

2026-07-09 对 82 个 GitHub DDL validation root-cause case 做回看,详见
`/Users/bba/pc/ai-native-ddl-github-heldout-methodology.md`。当前最强 run 加入 DDL docs/battery 后
是 FOUND=49,NOT_FOUND=29,UNCERTAIN=4;generic RE2 baseline 是 FOUND=42/82。这说明 DDL 方法论上下文
确实有效,但还不能覆盖所有历史 DDL bug。

最重要的修正:不要把 DDL 方法论收缩成单一的 reference ownership 矩阵。`DDL 改对象后引用必须
rewrite/block/cleanup` 仍然是高效主线,但 GitHub miss 暴露出另一组 DDL pipeline obligations:

```text
S-OBJ    object/reference ownership
S-ART    generated artifact owner/cardinality/type
S-STATE  job/task/subtask state and durable commit boundary
S-LIFE   resource lifecycle under success/error/cancel/owner switch
S-ERR    error identity/context preservation across wrap/retry/persist
S-RETRY  retry/input cursor idempotence for one-shot readers/requests
S-CFG    config/session/stale-input propagation into internal SQL
S-CACHE  schema/cache/side-metadata freshness
S-ENV    external topology/environment contracts: PD/S3/network/upgrade/cluster namespace
```

这组分类改变下一轮选靶:

- S-OBJ/S-ART 继续用小 SQL 矩阵和强行为 oracle。
- S-STATE/S-LIFE/S-ERR/S-RETRY 需要 failpoint 或错误注入;没有 hold 点时不能把 final-state GREEN
  外推到中间态。
- S-ENV 需要拓扑/外部系统 harness;普通源码 review 漏掉不代表 selector 无效,而是执行环境未覆盖。
- STRESS_PERF、TEST_ONLY、纯错误消息类 case 单独分层,不计入当前 high-value correctness 主线。

因此,历史 bug 不再只问"我们找到没有",而要给每个 miss 补四个字段:

```text
discoverability: SQL_ONLY / SOURCE_ONLY / FAULT_INJECTION / CLUSTER_TOPOLOGY / STRESS_PERF / LOW_VALUE
obligation:      S-OBJ / S-ART / S-STATE / S-LIFE / S-ERR / S-RETRY / S-CFG / S-CACHE / S-ENV
oracle_gap:      缺哪个强 oracle
selector_gap:    缺哪个可复用 selector
```

这能防止两个方向的漂移:一是继续在已绿的 reference matrix 上无效扩宽;二是把需要故障注入或拓扑的
bug 错判成"AI 没想出来"。

## 12. 注入粒度规则:恢复性验证 != 错误分类验证
2026-07-10 的 `MODIFY COLUMN` 校准给了一个很关键的边界:

```text
local semantic injection = RED
live coarse infra fault  = GREEN
```

具体并置:

- local:
  - `mockBackfillRunGrpcUnavailable` 单次注入
  - `ADD INDEX` retry + PASS
  - `MODIFY COLUMN` fail + rollback
- live:
  - active `write reorganization` 窗口中
  - 当前 owner TiDB -> 全 TiKV `NetworkChaos 10s`
  - `row_count` 冻住后恢复推进
  - 最终 `synced` + final oracle GREEN

源码解释也足够清楚:

```text
toTError(err):
  foreign transient error -> synthesize CodeUnknown

ADD INDEX path:
  isRetryableError(err, true)

MODIFY COLUMN path:
  isRetryableError(err, false)
```

因此 loop 里要显式分开两类注入:

1. **粗粒度基础设施故障**
   - 例: owner delete / pod bounce / owner->TiKV network partition
   - 主要验证:
     - `S-STATE`
     - `S-LIFE`
     - observer 是否真的命中 active window
     - 系统是否具备恢复性

2. **bridge-proximal 语义故障**
   - 例: 单次 worker return error / 单次 grpc unavailable / error wrap 前后身份丢失
   - 主要验证:
     - `S-ERR`
     - `S-RETRY`
     - error identity 是否跨 bridge / persist / retry classifier 被保留

新的执行规则:

- `local semantic RED + live infra GREEN` 不是假阳性,而是**注入粒度不匹配**。
- 这时不要继续盲目加大网络/owner 故障矩阵。
- 应该创建一个更近 error bridge 的 harness 任务,例如:

```text
BackfillData -> toTError -> isRetryableModifyColumnReorgJobError
```

- 只有当 bug 主张本来就是 `S-STATE/S-LIFE/S-ENV` 时,粗故障绿样本才足以否掉候选。

这条规则的本质是:我们验证的不是“网络抖一下会不会出事”这种泛命题,而是某个**证明义务**到底失守在
恢复链、状态机、还是错误身份桥上。注入点要跟证明义务对齐。

# common reorg ADD INDEX 动态缩容会吞掉被移除 worker 的真实错误,并发布缺行索引

## 这里的 in-flight 操作具体是什么

它不是抽象的“某个错误”。一个真实且容易出现的来源是订单写入的
ambiguous commit:第一次 INSERT 已经提交,客户端在收到响应前超时,应用用新连接
重试,而表当时还没有 `order_no` 唯一约束,于是同一个订单号落到两个不同的
自增 `id`。之后在线补唯一索引时,txn backfill 的 `batchCheckUniqueKey` 在处理
这两个订单号所在的 batch 时返回 `kv.ErrKeyExists`,用户侧就是普通的:

```text
ERROR 1062 (23000): Duplicate entry 'SO-20260712-0042' for key 'orders.uk_order_no'
```

这条错误本来应该让 DDL rollback。真实环境里,大表正在
`write reorganization`,前台延迟上升,DBA 或资源控制自动化把 job 从 8 个 worker
降到 1 个:

```sql
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

如果包含重复订单号的后段 worker 正好在缩容时被 cancel,但仍完成当前 batch 并
返回 `ERROR 1062`,它就是这里说的 in-flight terminal result。只有这份错误被
吞掉、DDL 仍变成 `synced/public`,随后 `IGNORE INDEX` 与 `FORCE INDEX` 看到的
订单集合不一致或 `ADMIN CHECK TABLE` 报 `8223`,才是 bug。正常的
`ERROR 1062` + rollback 是 control。

## TL;DR

在 testbed `8220955` 上,对普通 txn-backfill `ADD INDEX` 做一个很小但非常有语义的控制面扰动:

1. 让某个 backfill worker 在一批数据已经做完之后,延迟 10 秒再返回真实错误
2. 在这 10 秒窗口里把 `tidb_ddl_reorg_worker_cnt` 对应的 job thread 从 `4` 动态缩到 `1`

结果不是正常 rollback,而是:

1. DDL 仍然 `synced/public`
2. 发布后的索引缺行
3. `ADMIN CHECK TABLE` 直接报 `8223`
4. 普通查询已经会走坏索引并返回错误结果

这是一条真正的 severe wrong-result / data-consistency bug,不是 moderate wrong-error。

## 更容易在真实环境出现的触发例子

最容易解释、也最接近线上操作的一种形状是: `orders` 表长期被旧 importer 和新
服务共同写入,旧 importer 在超时后重试,把同一个业务 `order_no` 写到了两个不同的
自增 `id` 下。发布前团队补唯一约束:

```sql
SELECT order_no, COUNT(*)
FROM orders
GROUP BY order_no
HAVING COUNT(*) > 1;

ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no);
```

为了让窗口足够长,表应是百万级并且重复 `order_no` 位于较晚的主键范围;同时要走
普通 txn backfill,例如关闭 `tidb_ddl_enable_fast_reorg` 或确认 ingest 不可用。DDL
进入 `write reorganization` 且 `row_count` 还在增长时,白天前台延迟升高,DBA 或资源
控制自动化执行一次受支持的降级:

```sql
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

这里的 “in-flight 操作” 不是抽象的“某个错误”:具体是
`addIndexTxnWorker.BackfillData` 在一个批次里检查两个相同 `order_no` 时,
`batchCheckUniqueKey` 返回 `kv.ErrKeyExists`,客户端看到普通的:

```text
ERROR 1062 (23000): Duplicate entry '<order_no>' for key 'orders.uk_order_no'
```

如果这个忙 worker 恰好是被缩容取消的 tail worker,该终止结果可能在
`sendResult` 的 `ctx.Done()` 分支被丢掉。命中后的强判据是 DDL 错误地成为
`synced/public`,但两条路径看到的订单集合不同:

```sql
SELECT * FROM orders IGNORE INDEX(uk_order_no)
WHERE order_no = '<duplicate-order-no>';
SELECT * FROM orders FORCE INDEX(uk_order_no)
WHERE order_no = '<duplicate-order-no>';
ADMIN CHECK TABLE orders;
```

正常 control 是 `ERROR 1062` 后 rollback;没有 failpoint 的线上形状仍受时序影响,
不能替代下面的确定性 reproducer。它的价值是把错误来源和真实运维动作都落到
普通业务数据上,而不是停在 “in-flight 操作返回必须处理的错误或结果”。

## 用户层表现

命中的 live run:

- schema: `ai_native_shrink_test2`
- add-index job: `4452`
- table rows: `32768`

DDL 终态:

- `ADMIN SHOW DDL JOBS 5` 显示 `job 4452 ... add index ... state=synced`
- job config 字段为 `txn, thread=1, batch_size=32, max_node_count=3`

但是表已经坏了:

```sql
USE ai_native_shrink_test2;

SELECT COUNT(*) FROM t;                    -- 30301
SELECT COUNT(*) FROM t IGNORE INDEX(idx_a); -- 32768
SELECT COUNT(*) FROM t FORCE INDEX(idx_a);  -- 30301

ADMIN CHECK TABLE t;
-- ERROR 8223 (HY000): data inconsistency in table: t, index: idx_a, handle: 5676, ...
```

具体 witness:

```sql
SELECT COUNT(*) FROM t IGNORE INDEX(idx_a) WHERE a = 5676; -- 1
SELECT COUNT(*) FROM t FORCE INDEX(idx_a)  WHERE a = 5676; -- 0
SELECT COUNT(*) FROM t WHERE a = 5676;                     -- 0
```

`EXPLAIN FORMAT='brief' SELECT COUNT(*) FROM t;` 显示普通查询已经选择 `IndexFullScan(idx_a)`;也就是说,这不是“只有手工强制索引才看得见”的后台损坏,而是用户默认查询就会错。

## 为什么这是高质量 severe bug

这条 bug 的后果链非常硬:

- 不是 `ALTER` 返回错误但能重试
- 不是 job 卡住但数据没坏
- 不是 metadata stale 但普通查询没受影响

而是:

```text
动态缩容
-> in-flight worker 真实错误未被收集/传播
-> DDL 错误发布成功
-> 不完整索引进入 public
-> 默认查询走坏索引
-> 用户拿到错误结果
```

## 一个更容易在真实环境出现的例子

“重复业务键”在生产里并不一定来自手工造脏数据，常见来源之一是**提交结果不明确
时的客户端重试**：第一次请求实际上已经提交，但客户端在收到响应前超时，随后用新
连接再次写入。表上还没有唯一约束时，两次写入都会成功：

```sql
CREATE TABLE orders (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  order_no VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  payload VARCHAR(128) NOT NULL
);

-- 第一次请求：服务端已提交，但客户端没有收到成功响应。
INSERT INTO orders(order_no, status, payload)
VALUES ('SO-20260712-0042', 'paid', 'import-attempt-1');

-- 超时重试：新的连接再次插入同一业务键。
INSERT INTO orders(order_no, status, payload)
VALUES ('SO-20260712-0042', 'paid', 'import-retry-2');
```

把这张表换成已有的百万级、持续写入的业务表，且让这两个重复值落在较晚的主键
范围，就得到比较容易拉长窗口的真实形状。在线补唯一约束时走普通 txn backfill
（例如 `tidb_ddl_enable_fast_reorg=OFF` 或 ingest 不可用）：

```sql
ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no);
-- 另一个会话观察到 write reorganization 且 row_count 增长后：
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

这里不需要 TiKV 故障。晚范围 worker 在检查两个相同 `order_no` 时会自然产生
`ERROR 1062 Duplicate entry 'SO-20260712-0042'`；如果它正好属于被缩容的 tail，
就进入本 bug 的取消后结果投递窗口。DDL 结束后必须同时查两条路径：

```sql
SELECT id FROM orders IGNORE INDEX(uk_order_no)
WHERE order_no = 'SO-20260712-0042' ORDER BY id;
SELECT id FROM orders FORCE INDEX(uk_order_no)
WHERE order_no = 'SO-20260712-0042' ORDER BY id;
ADMIN CHECK TABLE orders;
```

无 failpoint 时这仍然是概率性触发：如果重复值先被检查到，正常的 `ERROR 1062`
和 rollback 是 control；只有 `synced/public` 后两条路径分裂或 `8223` 才是 bug。
这段例子补的是“错误从哪里来、谁触发缩容、用户最后看到什么”，不是把概率性窗口
包装成确定性复现。

## live 复现摘要

环境:

- testbed: `8220955`
- namespace: `testbed-tps-8220955-1-213`
- owner front: `127.0.0.1:14003`
- status / failpoint owner: `127.0.0.1:18086`

使用的临时 failpoint(本地补到 owner binary):

- `github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrForWorker`
- `github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrSleepMs`

语义:

- 指定某个 backfill worker
- 在 `BackfillData(...)` 已返回之后、merge result 之前
- 先 sleep 指定毫秒数
- 再返回 `mock backfill post-batch error on worker <id>`

复现形状:

1. `tidb_enable_dist_task=0`
2. `tidb_ddl_enable_fast_reorg=0`
3. `tidb_ddl_reorg_worker_cnt=4`
4. 建 32768 行表并 split 成 16 个 region
5. 在 owner 上挂:
   - `mockBackfillPostBatchErrForWorker=return(3)`
   - `mockBackfillPostBatchErrSleepMs=return(10000)`
6. 执行 `ALTER TABLE t ADD INDEX idx_a(a)`
7. 等 job 进入 `write reorganization` 之后,执行:
   - `ADMIN ALTER DDL JOBS <job_id> THREAD = 1`
8. 等 DDL 自己结束
9. 跑 `ADMIN CHECK TABLE` + `IGNORE INDEX`/`FORCE INDEX` 差分

## 关键对照

控制格已经证明“单纯注入错误”本身不是问题关键:

- `job 4442`
- 只注入 `worker0` post-batch error
- 不做 downscale

结果:

- DDL 直接报 `ERROR 1105 (HY000): mock backfill post-batch error on worker 0`
- history 为 `rollback done`
- owner log 明确出现 `backfill worker failed`

所以红点不在“注入错误能不能打坏 job”,而在:

**downscale 是否会把被移除 worker 的 in-flight 错误静默吞掉。**

## 更具体的真实触发场景

“in-flight 操作返回必须处理的错误”在用户侧可以是普通的历史脏数据,不需要人为制造 TiKV 故障。一个常见场景是:多次导入或应用重试曾经把同一个业务邮箱写入两行,现在运维要补建唯一约束。为了和当前 bug 的 `txn` backfill 路径一致,该集群需要是 fast reorg 关闭或 ingest 不可用的配置。

在当前 testbed `8220955` 的 `127.0.0.1:14101` 上,关闭 fast reorg、关闭 dist task、且不启用任何测试 failpoint,下面的普通 SQL 已经返回真实的 duplicate-key error:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 4;

CREATE DATABASE ai_native_real_duplicate_20260712;
USE ai_native_real_duplicate_20260712;
CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  email VARCHAR(255) NOT NULL,
  payload VARCHAR(32)
);
INSERT INTO users VALUES
  (81002344, 'a@example.com', 'import-a'),
  (81072319, 'a@example.com', 'import-b');

SELECT email, COUNT(*) FROM users
GROUP BY email HAVING COUNT(*) > 1;

ALTER TABLE users ADD UNIQUE INDEX uk_email(email);
-- ERROR 1062 (23000): Duplicate entry 'a@example.com' for key 'users.uk_email'
```

### 更接近生产的无 failpoint 触发例子

上面的四行表只是确认真实 `ERROR 1062` 会从 txn backfill 返回,它太小,作业会在运维来得及缩容前结束。真正容易把 race 窗口拉出来的线上形状是:表已经有百万级历史数据,重复值来自一次导入/应用重试,重复值位于较晚的主键范围,而运维在回填期间为了降低延迟把并发线程降下来。

这里的 “in-flight 操作” 可以具体到 TiDB 的正常执行步骤: `addIndexTxnWorker.BackfillData` 处理包含两行重复邮箱的 batch 时,`batchCheckUniqueKey` 发现两个不同 handle 对应同一个 `email`,返回 `ErrKeyExists`,最终就是用户熟悉的 `ERROR 1062 Duplicate entry`。它不是人为制造的 TiKV 故障;真正异常的是这个已经产生的终止结果在 worker 被缩容 cancel 后没有送到父 collector。

可以用下面的 SQL 构造同样的形状。生产中前面的百万行通常已经存在,不需要重新生成:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 8;

CREATE DATABASE ai_native_realistic_trigger_20260712;
USE ai_native_realistic_trigger_20260712;

CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  email VARCHAR(255) NOT NULL,
  payload VARCHAR(128)
);

CREATE TEMPORARY TABLE digit(n INT PRIMARY KEY);
INSERT INTO digit VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);

-- 1,000,000 条正常历史数据,让回填持续足够久并形成多个 region/task。
INSERT INTO users
SELECT
  d0.n + d1.n * 10 + d2.n * 100 + d3.n * 1000 +
  d4.n * 10000 + d5.n * 100000 + 1 AS id,
  CONCAT('user-', d0.n + d1.n * 10 + d2.n * 100 + d3.n * 1000 +
         d4.n * 10000 + d5.n * 100000 + 1, '@example.com') AS email,
  REPEAT('x', 128)
FROM digit d0, digit d1, digit d2, digit d3, digit d4, digit d5;

-- 模拟历史导入/重试留下的重复值,并故意放在较晚的主键范围。
INSERT INTO users VALUES
  (900000001, 'dupe@example.com', REPEAT('x', 128)),
  (900100001, 'dupe@example.com', REPEAT('x', 128));

SPLIT TABLE users BETWEEN (0) AND (1000000000) REGIONS 32;
```

然后从两个 session 操作。Session A 启动线上常见的“补唯一约束”操作:

```sql
USE ai_native_realistic_trigger_20260712;
ALTER TABLE users ADD UNIQUE INDEX uk_email(email);
```

Session B 每隔几秒观察一次 `ADMIN SHOW DDL JOBS`。看到该 job 仍是 `write reorganization`,`row_count` 还在增长时,执行一次降级:

```sql
ADMIN SHOW DDL JOBS;
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

命中的必要时序是:包含 `dupe@example.com` 的后段 region 正在由一个非前缀 worker 处理;线上为了保护前台延迟执行一次受支持的 `ADMIN ALTER DDL JOBS <job_id> THREAD = 1`;这个 worker 被放进 canceled tail;它在 `batchCheckUniqueKey` 中返回普通的 `Duplicate entry 'dupe@example.com'`;此时 `sendResult` 的 `ctx.Done()` 分支赢过结果发送,collector 没看到错误,DDL 错误地进入 `synced/public`。如果作业在缩容前已经发现重复值并 rollback,那是正常的 control,不是 bug;因此要用足够大的表和较晚范围来扩大这个窗口。

作业结束后用下面的 SQL 判断是否命中,而不是只看 `ALTER TABLE` 是否返回:

```sql
ADMIN SHOW DDL JOBS;
ADMIN CHECK TABLE users;

SELECT COUNT(*) FROM users IGNORE INDEX(uk_email);
SELECT COUNT(*) FROM users FORCE INDEX(uk_email);
SELECT COUNT(*) FROM users IGNORE INDEX(uk_email)
  WHERE email = 'dupe@example.com';
SELECT COUNT(*) FROM users FORCE INDEX(uk_email)
  WHERE email = 'dupe@example.com';
```

正常结果是 `ERROR 1062` 加 rollback,不会发布 `uk_email`;可疑结果是 job `synced/public`,但 table scan 与 `FORCE INDEX(uk_email)` 的行数/重复值结果不一致,或者 `ADMIN CHECK TABLE` 报 `8223`。这就是一个不需要 TiKV 故障、不需要测试 failpoint、只依赖“历史脏数据 + 在线建唯一索引 + 运维动态降并发”的真实环境触发例子。

一个更贴近线上变更的具体故事是: `orders` 表同时被旧版 importer 和新版服务写入,旧 importer 在超时重试时把同一个业务 `order_no` 写进了两个不同的自增 `id`。发布前团队执行 `ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no)` 来补上约束;这不是为了测试而故意制造坏数据,而是典型的历史数据治理/在线 schema migration。变更白天运行时前台延迟升高,DBA 或资源控制自动化执行一次受支持的 `ADMIN ALTER DDL JOBS <job_id> THREAD = 1`;如果重复 `order_no` 位于后段主键范围,就会形成“前面的 worker 已完成,后段 worker 仍在检查重复值,随后被缩容”的真实窗口。

用户侧的命中信号也要写具体:DDL history 显示 `synced/public`,但 `SELECT ... IGNORE INDEX(uk_order_no)` 能看到两行,`FORCE INDEX(uk_order_no)` 少一行,普通查询可能因为选中该索引而漏掉订单,并且 `ADMIN CHECK TABLE orders` 报 `8223`。如果 duplicate 在缩容之前被发现并正常 rollback,这是预期 control;真正的 bug 是同样的 `Duplicate entry '<order_no>'` 已经由 txn backfill 产生,却随 canceled tail worker 的 terminal result 一起丢失,最后发布了不完整索引。

这个无 failpoint 场景仍然是 timing-sensitive,不是 deterministic replacement。确定性 reproducer 继续使用上面的 failpoint,但它固定的不是虚构错误类型,而是把真实 `Duplicate entry` 发生在 downscale 之后的窗口固定住。

证据记录在 `assets/store/logs/add-index-real-duplicate-control-20260712.log`。

### 给复现条件的具体值班剧本

把“in-flight 操作返回必须处理的错误”展开成一条真实值班链路就是:

1. `orders` 没有唯一约束。旧 importer 写入 `SO-20260712-0042` 后,客户端在收到响应前超时;新连接重试,于是两个不同 `id` 下都有这个 `order_no`。
2. 发布前在线执行 `ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no)`,表是百万级且重复值在较晚主键范围,所以 txn backfill 会持续几分钟。
3. 前台延迟上升,DBA 执行受支持的 `ADMIN ALTER DDL JOBS <job_id> THREAD = 1` 来降低后台压力。
4. 正在处理后段范围的 worker 被 cancel,但它的当前 batch 仍在执行;`batchCheckUniqueKey` 发现两个相同 `order_no`,返回普通 `kv.ErrKeyExists`,也就是用户熟悉的 `ERROR 1062 Duplicate entry ...`。
5. 正常结果应该是 rollback。只有当这份 `ERROR 1062` 跟着被取消 worker 的 terminal result 一起丢失,DDL 仍进入 `synced/public`,并出现 `IGNORE INDEX` 与 `FORCE INDEX` 行集不一致或 `ADMIN CHECK TABLE` 的 `8223`,才是本 bug。

这条剧本不依赖 TiKV 故障、panic 或测试专用错误;failpoint 只负责把“重复键已经发生,但发生在 downscale 之后”这个时序固定下来。重复键若在缩容前被发现并 rollback,是 control,不能算命中。

## 关键日志链

来自 owner log `/tmp/fp-4003-dynamic.log` 的关键信号:

```text
14:16:23.095  adjust ddl job config success    current worker count=1
14:16:31.142  mock backfill post-batch error injected workerID=3 taskID=2
14:16:31.142  backfill worker exit on error    worker 3
14:16:31.142  backfill workers successfully processed total added count=30269
14:16:31.142  run reorg job done               jobID=4452 handled rows=30269
14:16:31.210  finish DDL job                   state=synced/public
```

最关键的是中间缺失的那一环:

- 没有 `backfill worker failed`
- 没有 rollback
- 没有 error return 给 job

而对照 job `4442` 上同类注入则明确出现:

```text
mock backfill post-batch error injected workerID=0
backfill worker failed ... error="mock backfill post-batch error on worker 0"
run reorg job done ... error="mock backfill post-batch error on worker 0"
```

## 当前最像真的根因

这条 live 证据和代码路径现在已经能基本对上:

1. `txnBackfillExecutor.adjustWorkerSize()` 缩容时会直接保留 `b.workers[:workerCnt]`,并对尾部 worker 调 `closeBackfillWorkers(...)`
2. `closeBackfillWorkers(...)` 只是 `worker.cancel()`,不会等待该 worker 把当前 in-flight result 送出来
3. `backfillWorker.sendResult(...)` 的实现是:

   ```go
   select {
   case <-w.ctx.Done():
   case w.resultCh <- result:
   }
   ```

   所以一旦 worker 的 context 先被 cancel,真实 error result 就会被静默丢掉
4. 外层 result collector 在 `resultChan` 被正常关闭且没有看到任何 `result.err` 时,会直接记:

   ```text
   backfill workers successfully processed
   ```

   然后返回 `nil`

这正好解释了命中的 live 顺序:

- `job 4452` 里,workers `0/1/2` 先自然跑完并退出
- 缩容到 `1` 时,executor 逻辑保留的是切片前缀里的 `worker0`,而不是“当前还在干活的那个 worker”
- 真正还在跑的 `worker3` 被 cancel
- `worker3` 在 sleep 结束后确实产生了真实 error,日志也打出了 `mock backfill post-batch error injected`
- 但它随后走到 `sendResult(...)` 时,由于 `w.ctx.Done()` 已经成立,这份错误没有进入 result collector
- collector 只看到 channel 最终被关闭,于是把整个 backfill 当成成功并发布

换句话说,这里不是泛泛的“并发有竞态”,而是一个相当具体的组合:

```text
downscale keeps worker slice prefix
+ cancels busy tail worker
+ sendResult drops result on canceled worker ctx
+ collector treats clean channel close as success
= published incomplete index
```

## Update 2026-07-11: 更系统的 sibling 说明这不是“所有 canceled tail error 都会坏”

为了把 reproducer 从偶然的 `worker3` 收紧成更一般的 selector,这轮又补了两组更系统的 live sibling:

1. `mockBackfillPreSendErrForWorker=tail + sleep 10s + thread=1 downscale`
2. `mockBackfillPostBatchErrForWorker=tail + before-send sleep 10s + thread=1 downscale`

它们都明确打到了 tail-worker error / downscale / retry 这一条路径,但都**没有**长出坏索引:

- `job 4487`:
  - `mock backfill pre-send error injected` 命中 `worker1/2/3`
  - collector 明确看到 `backfill worker failed`
  - job 中途 `ErrCount=1`
  - 最终 retry 后 `synced`
  - `COUNT(*) / IGNORE INDEX / FORCE INDEX` 全等,`ADMIN CHECK TABLE` 绿
- `job 4492`:
  - `mock backfill post-batch error injected` 命中 `worker1/2/3`
  - 同时 `mock backfill before-send sleep injected ... resultHasErr=true`
  - downscale 已在 sleep 期间发生
  - 但错误最终仍被 collector 看见,job retry 后 `synced`
  - 连跑 3 次 batch sibling(`job 4497 / 4502 / 4507`)都保持 end-state 绿

这几组格子很值钱,因为它们把当前 severe root 再收紧了一层:

```text
不是“任何 canceled tail worker error 都会 silent publish”
而更像是
“某个更窄的 sendResult 竞争窗口里,
ctx.Done 分支偶发赢过 resultCh send,
导致 error/result 被静默丢掉”
```

换句话说,`sendResult(...)` 里的:

```go
select {
case <-w.ctx.Done():
case w.resultCh <- result:
}
```

不是稳定地走某一边,而是一个真正的竞争点:

- 这轮系统 sibling 里,`resultCh <- result` 这边赢了,于是系统走了**可恢复的 retry-safe path**
- 原始 severe hit(`job 4452`)里,更像是 `<-w.ctx.Done()` 这边赢了,于是系统走了**silent drop -> published wrong-result path**

### 新增方法论收获

这轮最重要的不是又挖出一个新 bug,而是把 current root 从“downscale 吞错”进一步压成:

```text
动态控制面动作
+ in-flight worker result-delivery race
+ retry / checkpoint / final publish split
```

也因此,下一步最值得做的不是盲目再换模块,而是继续在这一条 lane 上:

1. 想办法**直接观测 sendResult 最终走了哪一边**
2. 把 batch harness 跑成一个小分布,区分:
   - safe retry
   - silent drop + wrong-result
3. 再决定这个 selector 是只属于 add-index txn-backfill,还是值得迁到 `UPDATE COLUMN` 等同 executor owner

换句话说,这里被打破的 proof obligation 是:

```text
只要任一 in-flight worker 已经产生真实错误
-> job 就必须看到该错误
-> 必须 rollback
-> 绝不能 publish
```

## 方法论价值

这条不是“再多跑几组 SQL”自然出来的,而是下面这个 loop 命中的:

1. 从源码/历史 review 里先找 serious obligation
   - 动态缩容后,worker error 是否仍 guaranteed delivery
2. 把它压成小矩阵
   - 有 downscale / 无 downscale
   - 同一注入点,同一 worker family
3. 用强 oracle 验证
   - `ADMIN CHECK TABLE`
   - `IGNORE INDEX` vs `FORCE INDEX`
   - 默认 plan 是否已经走坏索引
4. 命中后立即暂停,反推 selector
   - `动态控制面动作 × in-flight error/result acceptance`

它说明后续 severe DDL 挖掘里,以下维度应该上升为一等搜索单元:

- `thread downscale / upscale`
- `pause / cancel`
- `owner handoff`
- `checkpoint merge / result acceptance`

因为这些动作会直接改变“系统是否仍然相信某个先验条件成立”。

## 下一步最值得补的两格

1. 补一个更尖的 retained-worker sibling:
   - 让 `worker0` 持有同类 post-batch error
   - 仍然做 `thread=1` downscale
   - 如果它稳定 rollback,就能把根因进一步收紧到“被 cancel 的 busy tail worker 才会丢错”
2. 尝试把必要条件继续压小
   - 缩短 sleep
   - 缩少 region 数
   - 验证是否必须命中 tail worker,还是任意被 cancel 的 in-flight worker 都会掉

## 当前判断

- Status: confirmed candidate with severe user impact
- Severity: high / severe
- Area: DDL / common reorg / txn-backfill / dynamic worker downscale
- User impact: published wrong-result, default query can miss rows
- 建议:尽快入 bug 库,并准备对外 issue

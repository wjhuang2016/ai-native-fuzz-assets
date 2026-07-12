# common reorg ADD INDEX 动态缩容会吞掉被移除 worker 的真实错误,并发布缺行索引

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

生产中把这两行放在一个大表较晚的主键范围,让 `ADD UNIQUE INDEX` 运行数分钟并启动多个 worker。作业进入 `write reorganization` 后,如果前台延迟或 TiKV 资源压力上升,运维可能执行:

```sql
ADMIN SHOW DDL JOBS;
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

此时具体的 red 条件是:包含重复邮箱的后段范围正在被 worker 处理,该 worker 因 8-to-1 downscale 落入被取消的 tail;它在 cancel 之后返回 `Duplicate entry 'a@example.com'`,但 `sendResult` 选择 `ctx.Done()` 分支,collector 没看到错误,DDL 错误地进入 `synced/public`。正确结果应当是 duplicate-key error + rollback。这段无 failpoint 的 SQL 是真实错误来源的 control,并不能替代时序敏感的 deterministic failpoint reproducer。

证据记录在 `assets/store/logs/add-index-real-duplicate-control-20260712.log`。

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

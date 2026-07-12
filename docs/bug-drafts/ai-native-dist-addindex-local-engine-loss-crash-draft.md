# distributed ADD INDEX 在 import 前丢失 local engine 目录会打掉执行 TiDB 进程

## TL;DR

在 testbed `8220955` 上,对 distributed `ADD INDEX` 的 live failpoint owner 做一次很小的 runtime 资产扰动:

- 先把 DDL 卡在 `SetTSBeforeImportEngine` 之后、真正 `import` 之前
- 再删除本地 engine 目录
- 然后放行 import

结果不是普通 SQL 错误,而是:

1. 执行 `ALTER TABLE ... ADD INDEX` 的前台连接直接 `Lost connection to MySQL server during query`
2. 承载该 subtask 的 TiDB front(`4000/10080`)直接消失
3. 另一个 TiDB 在约 1 分钟后接管 owner 并最终把 DDL 补完

这是一条高价值 availability bug candidate:系统最终能自愈,但对用户来说是一次真实的 TiDB 进程退出,不是普通可见错误返回。

## Update 2026-07-11: engine DB 内部单个 SST 文件已经足够

前面最早拿到的是“删整个 local engine 目录会打掉执行 TiDB 进程”的 live 证据;这轮又把必要扰动继续压小了一层。这里的
“单个 SST”是 Pebble **local engine DB 内部**的 `000004.sst`,路径形如
`<job_id>/<engine_uuid>/000004.sst`,不是 ingest 输入目录里的
`<engine_uuid>.sst/<uuid>.sst`。后者的删除已经单独做过 GREEN control:TiDB 把它变成
`context canceled`/retry,重建任务后正常完成。

新的更强 live 形状:

- owner / execution node: `4001`
- failover survivor: `4003`
- `tidb_max_dist_task_nodes=1`
- hook 仍然是 `pauseAfterSetTSBeforeImportEngine`

在 `job 4282 / global task 420001` 上,只删除了 local engine DB 内部的:

- `/tmp/fp-survivor-20260711/tmp_ddl-4001/4282/09ae5021-ec10-5ca9-8a0d-fbeb2e371e16/000004.sst`

结果仍然是同一个高价值用户层表现:

1. `ALTER TABLE ... ADD INDEX` 直接报 `ERROR 2013 (HY000): Lost connection to MySQL server during query`
2. 执行 subtask 的 `4001` TiDB 进程直接消失
3. `4003` 随后接管 owner 并把同一个 `task-id=420001` 补完
4. 最终 `job 4282` 在 `2026-07-11 13:21:52 UTC` `synced`,`idx_b` 出现

这把结论从“目录级 runtime asset loss 会打崩 import front”收紧成了:

```text
local engine DB 内部单 SST 文件级 runtime asset loss
-> import path fatal
-> TiDB front exit
-> owner failover
-> delayed self-heal
```

## Update 2026-07-12: current-master 再次复现并补齐绿色边界

在同一 testbed 的 current dirty build 上重新跑了 `job 5151 / task 630002`:

- `4000` 在 `SetTSBeforeImportEngine` 之后被 pause;
- 删除 `/tmp/tidb/tmp_ddl-4000/5151/0918a1ff-0528-5d8e-ab55-616bfa336277/`,其中含 `000004.sst`;
- 放行后 `4000` 的 DDL 前台返回 `ERROR 2013 Lost connection`,进程从 `/proc` 消失;
- owner 日志出现 `orig err`/`list err` 的缺文件信息;
- DXF balancer 把同一个 `task-id=630002` 从 `exec_id=4000` 移到 `4001`,并最终完成;
- `job 5151` 在 `11:18:23 UTC` 变成 `synced/public`;
- table scan、`FORCE INDEX`、default scan 均为 `10000`,`ADMIN CHECK TABLE` 通过。

同轮的对照是 `job 5148`:在 `pauseBeforeLocalDBIngest` 删除 raw input SST 后,TiDB 没有退出,
而是把 subtask 标成 retryable,从空目录重新扫描并完成。这个对照很重要:不能把“输入文件丢失”
和“已打开 Pebble DB 引用的内部 SST 丢失”混成同一个资产。

当前远端 bug 库记录为 `id1530002` (`candidate/high`,
`root_cause_id=dist-addindex-local-engine-db-loss-process-exit`),但仍保留产品契约门:
需要确认临时 DDL engine 的损坏是否允许 fail-fast,以及是否应该由 Pebble logger/DDL 边界把它
转换为 subtask error,而不是杀掉 serving TiDB。证据:
`assets/store/logs/add-index-local-engine-db-loss-red-20260712.log`。

## 实验卫生补充

这轮还暴露了一个很实用的方法论细节:一度看上去像是“4003 接管后仍然挂死”,但后面确认那不是新的产品 bug,而是实验残留:

- 我们自己曾在 `4003` 的 failpoint HTTP(`23390`) 上留下了同一个 `pauseAfterSetTSBeforeImportEngine`
- 所以 `4003` 接管后也停在同一 pause 点
- 清掉 `23390` 上的 pause 后,日志立刻继续:
  - `import start`
  - `import engine success`
  - `run subtask completed`

这条经验很值得固定进 LOOP:

1. failover / retry / recovery 类实验,要把**所有候选 executor 的 failpoint state**一起审计
2. 只有先排除实验残留,后面的“接管后还卡住”才值得上升为产品侧 liveness root

## 用户层表现

- 前台 `ALTER TABLE ... ADD INDEX` 直接断连接
- DDL 在一段时间内保持 `running/write reorganization`
- owner failover 后任务最终恢复并 `synced`
- 最终索引会出现,所以这不是 silent wrong-result,而是 crash + delayed self-heal

## live 复现摘要

环境:

- testbed: `8220955`
- namespace: `testbed-tps-8220955-1-213`
- failpoint owner front: `127.0.0.1:14000` -> pod `fp-tidb:4000`
- current-master front: `127.0.0.1:14001` -> pod `fp-tidb:4001`

关键 hook:

- `github.com/pingcap/tidb/pkg/ingestor/ingestctrl/pauseAfterSetTSBeforeImportEngine`

最小步骤:

1. 把 DDL owner 切到 failpoint build `4000`
2. 开启 `pauseAfterSetTSBeforeImportEngine=1*pause`
3. 建表并灌入 2w 行
4. 执行 `ALTER TABLE t ADD INDEX idx_a(a)`
5. 在 pause 窗口内删除:
   - `/tmp/fp-reload-20260711d/tmp_ddl-4000/<job_id>/<engine_uuid>`
6. 释放 pause

## 两次 live 证据

第一次:

- job `4172`, global task `330001`
- `ALTER` 前台连接丢失
- `4000/10080` 监听消失
- 最终任务历史显示:
  - `330001 backfill succeed`
  - `state_update_time = 2026-07-11 11:49:06`

第二次:

- job `4179`, global task `360001`
- `ALTER` 前台连接再次丢失
- `4000/10080` 再次消失
- 中间观测到:
  - `4001` 已重新成为 DDL owner
  - 表上还没有 `idx_a`
  - 当前 task 仍显示 `running`,且 `exec_id=10.200.16.101:4000`
- 随后任务历史显示:
  - `360001 backfill succeed`
  - `state_update_time = 2026-07-11 11:54:17`
  - subtask history 最终落在 `exec_id=10.200.16.101:4001`

## 关键日志信号

两次 owner log 都在 `import start` 后停在同一类真实文件缺失:

```text
orig err: open .../000004.sst: no such file or directory
list err: open .../<engine_uuid>: no such file or directory
```

之后 4000 进程消失,本地 `14000` 再连会报:

```text
ERROR 2013 (HY000): Lost connection to MySQL server at 'reading initial communication packet'
```

## 更强的 live 闭环

这次最关键的收口,不只是“客户端断了”,而是可以把**进程死亡**和**后续自愈**分开看清:

- `fp-tidb` pod 的 PID1 只是 `sleep infinity`,不是 TiDB supervisor
- 所以 pod 继续 `Running`,并不能说明 `4000` 上那条 TiDB 进程还活着
- 事后在 pod 里查 `/proc` 与监听端口,只剩下 `4001/10082` 对应的 TiDB front;`4000/10080` 已经消失
- `/tmp/fp.log` 与 `/tmp/fp.repro.log` 都在同一个 `orig err/list err` 缺文件位置戛然而止
- `/tmp/tidb-live2.log` 则在稍后记录了 `4172/330001` 与 `4179/360001` 的继续推进、收尾和 `server count: 1`

换句话说,这里不是“同一个前台报错后继续跑完”,而是:

```text
4000 进程被打掉
-> 4001 接管 owner
-> DXF/DDL 在单前台状态下继续收敛
```

## 源码闭环

现在这条 candidate 的 source chain 也基本闭环了:

1. TiDB ingest 会把 raw SST 路径交给 Pebble:
   - `pkg/ingestor/ingestctrl/engine.go`
   - `dbSSTIngester.ingest(...) -> db.Ingest(paths)`
   - raw input SST 缺失时,`db.Ingest` 的 `FS.Open` 返回普通错误;本轮 GREEN control 证明上层会 retry。
2. TiDB 打开 local engine 的 Pebble DB 时没有自定义 `Logger`:
   - `pkg/ingestor/ingestctrl/engine_mgr.go`
3. Pebble 默认会把空 `Logger` 补成 `DefaultLogger`:
   - `github.com/cockroachdb/pebble/options.go`
4. `db.Ingest(paths)` 期间,已打开的 Pebble DB version 仍引用 engine DB 内部的 table file;
   读取这个缺失的内部 SST 会走 `OpenOptions{MustExist: true}`:
   - `github.com/cockroachdb/pebble/table_cache.go`
   - `github.com/cockroachdb/pebble/objstorage/objstorageprovider/vfs.go`
5. `MustExist(...)` 一旦命中 `ENOENT`,会打印与 live 完全同形状的:
   - `orig err: ...`
   - `list err: ...`
   然后走 `Fatalf(...)`
6. `DefaultLogger.Fatalf(...)` 最终就是 `os.Exit(1)`:
   - `github.com/cockroachdb/pebble/internal/base/logger.go`

这解释了为什么“删 local engine 目录”不是普通 SQL error return,而会直接打掉执行 import 的 TiDB 进程。

## 为什么这条值得追

它不是纯 SQL 即可触发的 bug,而是 runtime 资产损坏类 bug:

- old loop 更擅长发现“SQL 语义 / proof obligation”类红点
- 这条需要 AI 主动改 harness、卡 phase、扰动运行时资产、再看 owner failover 和 DXF 恢复语义

也因此,它正好验证了新 loop 里那条更强的能力:

1. 先从源码里找出真正有语义意义的 pause 点
2. 在 pause 点做最小资产扰动
3. 用强 oracle 同时看前台症状、owner 生死、task 演化和最终 end-state

## 当前判断

- 质量:高价值 availability bug candidate,已两次 live 命中 engine-DB-loss -> process-exit
- 远端资产:`id1530002`, `candidate/high`;尚未按 confirmed root 计数
- 形态:local engine runtime loss -> import path fatal -> TiDB process exit -> owner failover -> delayed self-heal
- 还需要的下一步:
  - 评估这是“合理的致命保护”还是“应该返回错误而不该打掉进程”
  - 对照不同 Pebble logger/engine boundary 的修复位置,决定是否升级成正式 severe root
  - 若要对外提 issue,复现里要明确说明这是一条 runtime fault-injection / asset-loss 场景,不是纯 SQL 场景

## Bug Report

### 1. Minimal reproduce step (Required)

#### Production trigger

This can occur in a normal entity synchronization or migration job. A staging table is keyed by a
stable work item (`slot`) and stores the external entity ID that must be written into a materialized
table. The materialized table uses an `AUTO_INCREMENT` primary key, but, as supported by MySQL and
TiDB, the synchronization statement supplies explicit IDs:

```sql
INSERT INTO entity(id, payload)
SELECT target_id, payload FROM reconciliation_buffer ORDER BY slot
ON DUPLICATE KEY UPDATE payload = VALUES(payload);
```

The production ordering is:

1. A batch synchronization statement starts in autocommit mode and reads the old mapping
   `slot=2 -> target_id=100, payload='old'`. A large source scan, expression work, normal storage
   latency, or backoff keeps the statement in flight.
2. A normal incremental reconciliation transaction corrects that stable staging item to
   `slot=2 -> target_id=200, payload='new'`. The same transaction updates an existing hot entity
   row that is also covered by the batch, then commits.
3. The batch finishes its first attempt from the old snapshot. Its prewrite conflicts on the hot
   entity row, so TiDB transparently retries the whole autocommit statement.
4. The retry reads `target_id=200, payload='new'`, but `RetryInfo.autoIncrementIDs` supplies the
   first attempt's positional ID `100` before TiDB examines the current explicit ID `200`.
5. The SQL statement returns success and commits `(id=100, payload='new')`.

No TiDB/TiKV failure, failpoint, DDL, explicit transaction, or non-default SQL variable is required.
Classic TiDB's default `pessimistic-auto-commit=false` makes ordinary autocommit DML optimistic;
`tidb_retry_limit=10`, `autocommit=1`, and MDL ON are all verified by the test. One healthy TiDB,
one healthy TiKV, and two SQL sessions are sufficient.

Add `tests/realtikvtest/txntest/ai_native_autoid_retry_test.go`:

```go
package txntest

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAutoIDRetryDoesNotMixOldIDWithNewPayloadRealTiKV(t *testing.T) {
	if !*realtikvtest.WithRealTiKV {
		t.Skip("requires real TiKV")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	competitor := testkit.NewTestKit(t, store)
	competitor.MustExec("use test")
	competitor.MustExec("drop table if exists ai_source, ai_target")
	competitor.MustExec("create table ai_source(slot bigint primary key, target_id bigint not null, payload varchar(32) not null)")
	competitor.MustExec("create table ai_target(id bigint auto_increment primary key, payload varchar(32) not null)")
	competitor.MustExec("insert into ai_source values (1, 1, 'from-one'), (2, 100, 'old')")
	competitor.MustExec("insert into ai_target values (1, 'base')")
	competitor.MustQuery("select @@tidb_enable_metadata_lock, @@autocommit, @@tidb_retry_limit").
		Check(testkit.Rows("1 1 10"))
	require.False(t, config.GetGlobalConfig().PessimisticTxn.PessimisticAutoCommit.Load())

	worker := testkit.NewTestKit(t, store)
	worker.MustExec("use test")
	done := make(chan error, 1)
	go func() {
		resultSets, err := worker.Session().Execute(context.Background(), `insert into ai_target(id, payload)
			select target_id, if(sleep(if(slot = 1, 2, 0)) = 0, payload, payload)
			from ai_source order by slot
			on duplicate key update payload = values(payload)`)
		for _, resultSet := range resultSets {
			if closeErr := resultSet.Close(); err == nil {
				err = closeErr
			}
		}
		done <- err
	}()

	time.Sleep(500 * time.Millisecond)
	competitor.MustExec("begin")
	competitor.MustExec("update ai_source set target_id = 200, payload = 'new' where slot = 2")
	competitor.MustExec("update ai_target set payload = 'competitor' where id = 1")
	competitor.MustExec("commit")
	require.NoError(t, <-done)
	require.GreaterOrEqual(t, worker.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(1))

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select * from ai_source order by slot").
		Check(testkit.Rows("1 1 from-one", "2 200 new"))
	fresh.MustQuery("select * from ai_target order by id").
		Check(testkit.Rows("1 from-one", "200 new"))
	fresh.MustQuery(`select s.id, s.payload, t.id, t.payload
		from (select target_id as id, payload from ai_source where slot <> 1) s
		left join ai_target t on t.id = s.id`).
		Check(testkit.Rows("200 new 200 new"))
	fresh.MustExec("admin check table ai_target")
}
```

Run it with one real TiKV:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0
go test -tags=intest \
  -run TestAutoIDRetryDoesNotMixOldIDWithNewPayloadRealTiKV \
  -count=1 -v ./tests/realtikvtest/txntest
```

`SLEEP` only makes the already described production interval deterministic. The write conflict,
transaction retry, source change, and durable commit are all real product operations.

### 2. What did you expect to see? (Required)

There are only two coherent one-attempt outcomes:

- If the batch wins before the reconciliation transaction, it writes `(100, 'old')`.
- If reconciliation wins before the batch, the batch writes `(200, 'new')`.

The tested ordering forces the second case after one internal retry. The successful statement must
therefore leave:

```text
source slot 2: target_id=200, payload=new
target row:    id=200, payload=new
```

### 3. What did you see instead (Required)

The statement returned success after a real `9007` write conflict and one transparent retry, but a
fresh session read:

```text
source slot 2: target_id=200, payload=new
target row:    id=100, payload=new
```

The assertion failed as follows:

```text
expected: [1 from-one]
          [200 new]
actual:   [1 from-one]
          [100 new]
```

The real-TiKV slow log recorded `Exec_retry_count: 1`, `Succ: true`, and
`IsExplicitTxn: false`. `ADMIN CHECK TABLE ai_target` remains green because this is semantic row
identity corruption, not physical index corruption.

`adjustAutoIncrementDatum` currently consumes `RetryInfo.GetCurrAutoIncrementID()` and returns
before parsing the current retry datum. The first attempt also stores explicit nonzero IDs in the
same positional cache as generated IDs. As a result, the retry treats the old explicit ID `100` as
an interchangeable generated allocation and overwrites current explicit ID `200`, while preserving
the new payload.

Moving current-datum classification before cache reuse, consuming cached IDs only for values that
currently require generation, and using the current explicit ID makes the exact real-TiKV schedule
pass while preserving `ExecRetryCount=1`. A complete fix should retain enough provenance to
distinguish generated IDs from user-supplied explicit IDs across retries.

This is distinct from #20629. That issue handled a growing retry rowset exhausting a buffer of
generated auto IDs and returning `Cannot get auto-id in retry`; #20659 made an empty buffer allocate
more IDs. This report is not buffer exhaustion and returns no error. It is the silent reuse of an
old explicit business ID for a new retry row value, producing a durable ID/payload combination that
no coherent execution can produce.

### 4. What is your TiDB version? (Required)

- TiDB current master: `2964713e267eac6eab92c4be53e9ad0641df2e9f`
- Real TiKV: `v9.0.0-beta.2.pre-nightly`
- Go: `go1.25.10 darwin/arm64`
- MDL: ON

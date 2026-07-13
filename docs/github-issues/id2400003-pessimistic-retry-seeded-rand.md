## Bug Report

### 1. Minimal reproduce step (Required)

Metadata locking remains enabled throughout this reproduction.

Create two identical tables:

```sql
DROP DATABASE IF EXISTS pessimistic_seeded_rand_retry;
CREATE DATABASE pessimistic_seeded_rand_retry;
USE pessimistic_seeded_rand_retry;

CREATE TABLE retry_key(id INT PRIMARY KEY, u INT UNIQUE);
CREATE TABLE control_key LIKE retry_key;
INSERT INTO retry_key VALUES (1, 10);
INSERT INTO control_key VALUES (1, 10);
SELECT @@global.tidb_enable_metadata_lock;
-- 1
```

In session A, start a pessimistic READ COMMITTED transaction. The constant seed makes the `RAND`
sequence deterministic. `SLEEP` only makes the concurrent schedule easy to reproduce:

```sql
USE pessimistic_seeded_rand_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;

UPDATE retry_key
SET u = IF(RAND(12345) < 0.8, 1, 2) + SLEEP(20) * 0
WHERE id = 1;
```

While session A is sleeping, run and commit the following in session B:

```sql
USE pessimistic_seeded_rand_retry;
SET tidb_txn_mode = 'pessimistic';
BEGIN;
INSERT INTO retry_key VALUES (2, 1);
INSERT INTO control_key VALUES (2, 1);
COMMIT;
```

After the `UPDATE` in session A returns, commit and inspect the table:

```sql
COMMIT;
SELECT * FROM retry_key ORDER BY id;
```

Run the same seeded expression once from the same final database state:

```sql
UPDATE control_key
SET u = IF(RAND(12345) < 0.8, 1, 2)
WHERE id = 1;
SELECT * FROM control_key ORDER BY id;
```

The following test provides a deterministic in-repository reproduction. Add it under
`pkg/executor/test/writetest`:

```go
package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestPessimisticRetryDoesNotChangeSeededRandUniqueKey(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table rand_key_retry (id int primary key, u int unique)")
	owner.MustExec("create table rand_key_control like rand_key_retry")
	owner.MustExec("insert into rand_key_retry values (1, 10)")
	owner.MustExec("insert into rand_key_control values (1, 10)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update rand_key_retry
			set u = if(rand(12345) < 0.8, 1, 2) + sleep(0.8) * 0
			where id = 1`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into rand_key_retry values (2, 1)")
	competitor.MustExec("insert into rand_key_control values (2, 1)")
	competitor.MustExec("commit")

	ownerErr := <-errCh
	if ownerErr == nil {
		owner.MustExec("commit")
	} else {
		owner.MustExec("rollback")
	}
	retryRows := owner.MustQuery("select * from rand_key_retry order by id").Rows()

	_, controlErr := owner.Exec(`update rand_key_control
		set u = if(rand(12345) < 0.8, 1, 2)
		where id = 1`)
	require.Error(t, controlErr)
	controlRows := owner.MustQuery("select * from rand_key_control order by id").Rows()

	require.Error(t, ownerErr,
		"hidden retry must not advance constant-seed RAND past the conflicting first value")
	require.Equal(t, controlRows, retryRows)
}
```

Run it with:

```bash
go test -tags=intest ./pkg/executor/test/writetest \
  -run TestPessimisticRetryDoesNotChangeSeededRandUniqueKey -count=1
```

### 2. What did you expect to see? (Required)

`RAND(12345)` starts from a deterministic sequence. A successful transparent retry should not
advance that sequence past values consumed only by a hidden failed attempt.

For a fresh execution, the first value is below `0.8`, so the submitted expression selects `u=1`.
After session B commits `u=1`, the `UPDATE` should return the duplicate-key conflict and leave row 1
unchanged. At minimum, TiDB should expose the conflict instead of silently committing a result from
the next RNG position.

### 3. What did you see instead (Required)

Session A reports success and commits the key selected by the second seeded value:

```text
retry UPDATE: success, affected rows = 1, Exec_retry_count = 1
retry rows:   (1, 2), (2, 1)
```

The direct control starts from the same final database state but returns:

```text
ERROR 1062 (23000): Duplicate entry '1' for key 'control_key.u'
control rows: (1, 10), (2, 1)
```

An independent value control shows the same sequence-position drift:

```text
single execution:  CAST(RAND(12345) * 1000000000 AS UNSIGNED) = 665703432
hidden retry:      committed value                              = 912825259
```

On real TiKV, the slow log records `Exec_retry_count=1`, `Exec_retry_time=20.001772209`,
`Query_time=40.00417287`, `Succ=1`, and `IsExplicitTxn=1`. The roughly 40-second query time also
shows that the 20-second expression was evaluated twice.

For a constant argument, `randFunctionClass.getFunction` creates one mutable `MysqlRng` before
execution. `builtinRandSig.evalReal` advances it through `Gen()`, while `builtinRandSig.Clone`
shallow-copies the same pointer. The pessimistic retry path rebuilds the executor from the already
advanced plan/expression state, so the successful attempt consumes the second seeded value.

As a counterfactual, recording that a mutable `RAND` generator was consumed and declining the
transparent retry afterward makes the same test return the original conflict and preserve both
rows. Merely deep-copying the RNG in `Clone` after the first attempt is too late and remains RED;
the state must be restored from statement entry or the replay must be declined.

### 4. What is your TiDB version? (Required)

- Real-TiKV SQL reproduction: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
- TiDB commit: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- Current-source local reproduction: `b8d04e17a2ca61eee1220c5ce2d641a376f75e9b`
- Global `tidb_enable_metadata_lock`: `ON`

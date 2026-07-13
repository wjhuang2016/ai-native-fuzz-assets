## Bug Report

### 1. Minimal reproduce step

Add the following test as
`pkg/dxf/importinto/conflictedkv/deleter_commit_error_test.go`:

```go
package conflictedkv

import (
	"context"
	goerrors "errors"
	"testing"

	tidbkv "github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/store/mockstore"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
	"go.uber.org/zap"
)

type commitErrorStorage struct {
	tidbkv.Storage
	err error
}

func (s *commitErrorStorage) Begin(opts ...tikv.TxnOption) (tidbkv.Transaction, error) {
	txn, err := s.Storage.Begin(opts...)
	if err != nil {
		return nil, err
	}
	return &commitErrorTxn{Transaction: txn, err: s.err}, nil
}

type commitErrorTxn struct {
	tidbkv.Transaction
	err error
}

func (txn *commitErrorTxn) Commit(context.Context) error {
	if err := txn.Transaction.Rollback(); err != nil {
		return err
	}
	return txn.err
}

func TestDeleteBufferedKeysReturnsCommitError(t *testing.T) {
	ctx := context.Background()
	store, err := mockstore.NewMockStore()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	key := tidbkv.Key("commit-error/conflict-key")
	value := []byte("still-present")

	txn, err := store.Begin()
	require.NoError(t, err)
	require.NoError(t, txn.Set(key, value))
	require.NoError(t, txn.Commit(ctx))

	commitErr := goerrors.New("injected commit error")
	deleter := &Deleter{
		store:  &commitErrorStorage{Storage: store, err: commitErr},
		logger: zap.NewNop(),
	}
	err = deleter.deleteBufferedKeys(ctx, []tidbkv.Key{key})
	require.ErrorIs(t, err, commitErr)

	readTxn, err := store.Begin()
	require.NoError(t, err)
	got, err := readTxn.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, value, got.Value)
	require.NoError(t, readTxn.Rollback())
}
```

Run:

```sh
go test ./pkg/dxf/importinto/conflictedkv \
  -run '^TestDeleteBufferedKeysReturnsCommitError$' -count=1
```

Current master fails with:

```text
Expected error with "injected commit error" in chain but got nil.
```

The transaction is definitely rolled back and the key is still present, but
`deleteBufferedKeys` returns nil.

For a user-level validation, run global-sort `IMPORT INTO` with
`ON_DUPLICATE_KEY='capture'` and `CHECKSUM_TABLE='off'` on this input:

```text
1,1,10
1,1,10
2,1,20
3,3,30
```

The table is:

```sql
CREATE TABLE t(
  id INT PRIMARY KEY CLUSTERED,
  u INT UNIQUE,
  v INT,
  INDEX iv(v)
);
```

Inject one retryable error before the first conflict-deletion transaction Commit and roll that
transaction back. This represents a transient commit failure such as a region leader change,
temporary region unavailability, or server-busy/transport error during conflict resolution.

On current source, the import returned:

```text
Status:         finished
Imported_Rows:  1
Result_Message: 3 conflicted rows.
```

But the access paths returned:

```text
PRIMARY:       (2,1,20), (3,3,30)
UNIQUE u:      (3,3,30)
SECONDARY iv:  (2,1,20), (3,3,30)
ADMIN CHECK:   ERROR 8223, index u, handle 2
```

A same-process second import after the one-shot fault was consumed returned only `(3,3,30)` on all
three access paths and passed `ADMIN CHECK TABLE`.

### 2. What did you expect to see?

`deleteBufferedKeys` must return the transaction Commit error. `deleteKeysWithRetry` should then
classify a transient error and retry the complete key batch. A successful import in capture mode
must delete all KVs related to the three conflicted rows, retain only `(3,3,30)`, and pass
`ADMIN CHECK TABLE`.

### 3. What did you see instead?

`deleteBufferedKeys` commits in a defer:

```go
func (d *Deleter) deleteBufferedKeys(ctx context.Context, keys []tidbkv.Key) error {
	// ...
	defer func() {
		if err == nil {
			err = txn.Commit(ctx)
		}
	}()
	// stage deletes
	return nil
}
```

Because the function result is unnamed, `return nil` fixes the returned value before deferred
functions run. The defer changes only the local `err`; it cannot change the function result.
`RunWithRetry` therefore sees nil, skips retry, and lets the conflict-resolution task publish
success even though none of that transaction's deletes committed.

Changing only the signature to a named result:

```go
func (d *Deleter) deleteBufferedKeys(ctx context.Context, keys []tidbkv.Key) (err error)
```

made the same one-shot error visible to `RunWithRetry`. The log showed exactly one retry at
`retry-count=0`; the next attempt committed the deletes, the import finished with only `(3,3,30)`,
all access paths agreed, and `ADMIN CHECK TABLE` succeeded.

### 4. What is your TiDB version?

- Current master: `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`
- Real PD/TiKV validation: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`

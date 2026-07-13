## Bug Report

### 1. Minimal reproduce step (Required)

Pipelined DML requires metadata locking to be enabled. Metadata locking remains enabled throughout
this reproduction; TiDB uses the pipelined transaction path when an eligible autocommit DML runs
with `tidb_dml_type='BULK'`.

The following client-go integration test deterministically loses the first **successful** primary
Commit response. It forwards Commit responses that contain a Region or key error, so an earlier
`CommitTsExpired` response is not mistaken for a durable-apply witness. After the successful
response is lost, subsequent Commit attempts return a transport error until the 500 ms Commit
context ends.

Add `integration_tests/pipelined_undetermined_probe_test.go` to client-go:

```go
// Copyright 2026 TiKV Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tikv_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/pingcap/kvproto/pkg/kvrpcpb"
	"github.com/pkg/errors"
	tikverr "github.com/tikv/client-go/v2/error"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/tikvrpc"
)

type postApplyCommitResponseLossClient struct {
	tikv.Client
	commitCalls  int32
	droppedApply int32
}

func (c *postApplyCommitResponseLossClient) SendRequest(
	ctx context.Context,
	addr string,
	req *tikvrpc.Request,
	timeout time.Duration,
) (*tikvrpc.Response, error) {
	if req.Type != tikvrpc.CmdCommit {
		return c.Client.SendRequest(ctx, addr, req, timeout)
	}
	atomic.AddInt32(&c.commitCalls, 1)
	if atomic.LoadInt32(&c.droppedApply) != 0 {
		return nil, errors.New("injected commit transport outage")
	}

	resp, err := c.Client.SendRequest(ctx, addr, req, timeout)
	if err != nil {
		return resp, err
	}
	commitResp := resp.Resp.(*kvrpcpb.CommitResponse)
	if commitResp.RegionError != nil || commitResp.Error != nil {
		return resp, nil
	}
	atomic.StoreInt32(&c.droppedApply, 1)
	return nil, errors.New("injected loss after primary commit was applied")
}

func (s *testPipelinedMemDBSuite) TestAINativePipelinedCommitResponseLossRED() {
	key := []byte("ai-native-pipelined-undetermined")
	value := []byte("durably-committed")
	innerClient := s.store.GetTiKVClient()
	client := &postApplyCommitResponseLossClient{Client: innerClient}
	s.store.SetTiKVClient(client)

	txn, err := s.store.Begin(tikv.WithDefaultPipelinedTxn())
	s.Require().NoError(err)
	s.Require().NoError(txn.Set(key, value))
	flushed, err := txn.GetMemBuffer().Flush(true)
	s.Require().NoError(err)
	s.Require().True(flushed)
	s.Require().NoError(txn.GetMemBuffer().FlushWait())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	commitErr := txn.Commit(ctx)
	s.Require().Error(commitErr)
	s.Require().Equal(int32(1), atomic.LoadInt32(&client.droppedApply))
	s.GreaterOrEqual(atomic.LoadInt32(&client.commitCalls), int32(1))

	if *withTiKV {
		s.store.SetTiKVClient(innerClient)
		s.Eventually(func() bool {
			reader, err := s.store.Begin()
			if err != nil {
				return false
			}
			defer reader.Rollback()
			readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
			defer readCancel()
			got, err := reader.Get(readCtx, key)
			return err == nil && string(got.Value) == string(value)
		}, 15*time.Second, 100*time.Millisecond, "post-apply value must be durable in real TiKV")
	}
	s.True(
		tikverr.IsErrorUndetermined(commitErr),
		"durable value is visible after Commit returned a non-undetermined error: %v",
		commitErr,
	)
}
```

Start one real TiKV and run the test from `client-go/integration_tests`:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0 --without-monitor

go test . \
  -run 'TestPipelinedMemDB/TestAINativePipelinedCommitResponseLossRED' \
  -count=1 -v \
  -with-tikv \
  -pd-addrs=http://127.0.0.1:2379
```

### 2. What did you expect to see? (Required)

Once client-go loses a successful response to the primary Commit request, the transaction outcome
is unknown to the caller. `Commit` should return `ErrResultUndetermined`, regardless of the raw
transport or Region error that eventually stops retrying.

TiDB maps that typed error to its SQL-level result-undetermined error and closes the client
connection. This prevents the already committed operation from being represented as a definite,
ordinary failure on a reusable connection.

### 3. What did you see instead (Required)

The test fails on current client-go:

```text
durable value is visible after Commit returned a non-undetermined error:
injected commit transport outage
```

The injecting client is removed before the final read. A fresh transaction using the real client
reads `durably-committed`, proving that real TiKV committed the value. The original `Commit` result
is nevertheless an ordinary transport error, and `tikverr.IsErrorUndetermined(commitErr)` is false.

`actionCommit.handleSingleBatch` correctly records primary RPC ambiguity in
`twoPhaseCommitter.undeterminedErr`. The ordinary 2PC finalizer, `commitTxn`, checks that side state
and promotes the raw error to `ErrResultUndetermined`. The pipelined branch instead calls
`commitFlushedMutations`, which returns the raw `commitMutations` error without the promotion.

TiDB only maps typed `ErrResultUndetermined` to `terror.ErrResultUndetermined`, and the server only
closes the connection for that SQL error. An application can therefore retry an autocommit,
non-idempotent bulk DML after receiving the ordinary failure, even though the first operation is
already durable. This can duplicate ledger, balance, or inventory changes.

A minimal counterfactual at the pipelined terminal boundary makes the same real-TiKV test pass while
leaving the committed value unchanged:

```go
if err = c.commitMutations(bo, &primaryMutation); err != nil {
	if c.getUndeterminedErr() != nil {
		return errors.WithStack(tikverr.ErrResultUndetermined)
	}
	return errors.Trace(err)
}
```

### 4. What is your TiDB version? (Required)

- TiDB: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
- TiDB commit: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- The TiDB session and global `tidb_enable_metadata_lock` values were both `ON`.
- TiDB-pinned client-go `661db4f5f4e85d1efe3a0f189fc80c564b7b573a` is affected.
- The RED also reproduces on current client-go master
  `01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b`.
- Real TiKV: `7ecce12e7573f7d4a392877b994fa6af80606369`, built 2026-07-13.

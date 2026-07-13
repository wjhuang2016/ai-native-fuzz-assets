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

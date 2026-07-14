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
	tikverr "github.com/tikv/client-go/v2/error"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/tikvrpc"
)

type commitTSExpiredOnceClient struct {
	tikv.Client
	commitCalls    atomic.Int32
	expiredReplies atomic.Int32
}

func (c *commitTSExpiredOnceClient) SendRequest(
	ctx context.Context,
	addr string,
	req *tikvrpc.Request,
	timeout time.Duration,
) (*tikvrpc.Response, error) {
	if req.Type != tikvrpc.CmdCommit {
		return c.Client.SendRequest(ctx, addr, req, timeout)
	}

	c.commitCalls.Add(1)
	commitReq := req.Commit()
	if c.expiredReplies.CompareAndSwap(0, 1) {
		return &tikvrpc.Response{Resp: &kvrpcpb.CommitResponse{
			Error: &kvrpcpb.KeyError{CommitTsExpired: &kvrpcpb.CommitTsExpired{
				StartTs:           commitReq.StartVersion,
				AttemptedCommitTs: commitReq.CommitVersion,
				Key:               commitReq.Keys[0],
				MinCommitTs:       commitReq.CommitVersion + 1,
			}},
		}}, nil
	}
	return c.Client.SendRequest(ctx, addr, req, timeout)
}

func (s *testCommitterSuite) TestAINativeCommitTSRetryRechecksUpperBound() {
	key := s.key("cached-table-lease-bound")
	innerClient := s.store.GetTiKVClient()
	client := &commitTSExpiredOnceClient{Client: innerClient}
	s.store.SetTiKVClient(client)
	defer s.store.SetTiKVClient(innerClient)

	txn := s.begin()
	s.Require().NoError(txn.Set(key, []byte("new-value")))
	var checkerCalls atomic.Int32
	txn.SetCommitTSUpperBoundCheck(func(uint64) bool {
		return checkerCalls.Add(1) == 1
	})

	commitErr := txn.Commit(context.Background())
	s.Error(commitErr, "a replacement commitTS outside the cached-table lease must be rejected")
	s.Equal(int32(2), checkerCalls.Load(), "every commitTS candidate must pass the lease checker")
	s.Equal(int32(1), client.expiredReplies.Load())
	s.Equal(int32(1), client.commitCalls.Load(), "the rejected replacement must not be sent to TiKV")

	reader, err := s.store.Begin()
	s.Require().NoError(err)
	defer reader.Rollback()
	_, err = reader.Get(context.Background(), key)
	s.True(tikverr.IsErrNotFound(err), "the transaction must remain aborted when its replacement commitTS exceeds the lease")
}

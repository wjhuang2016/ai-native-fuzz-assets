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

	"github.com/pingcap/failpoint"
	"github.com/tikv/client-go/v2/oracle"
	"github.com/tikv/client-go/v2/txnkv/transaction"
	"github.com/tikv/client-go/v2/util"
)

type aiNativeExpiringOracle struct {
	oracle.Oracle
	expireLocks atomic.Bool
}

func (o *aiNativeExpiringOracle) IsExpired(lockTS, ttl uint64, opt *oracle.Option) bool {
	if ttl == transaction.MaxTxnTimeUse || o.expireLocks.Load() {
		return true
	}
	return o.Oracle.IsExpired(lockTS, ttl, opt)
}

func (s *testAsyncCommitFailSuite) TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted() {
	baseOracle := s.store.GetOracle()
	expiringOracle := &aiNativeExpiringOracle{Oracle: baseOracle}
	s.store.SetOracle(expiringOracle)
	defer s.store.SetOracle(baseOracle)

	s.Require().NoError(failpoint.Enable("tikvclient/commitFailedSkipCleanup", "return"))
	defer func() {
		s.Require().NoError(failpoint.Disable("tikvclient/commitFailedSkipCleanup"))
	}()

	primary := []byte("ai-native-expired-primary-20260714")
	secondary := []byte("ai-native-expired-secondary-20260714")
	txn := s.beginAsyncCommit()
	s.Require().NoError(txn.Set(primary, []byte("v1")))
	s.Require().NoError(txn.Set(secondary, []byte("v2")))
	ctx := context.WithValue(context.Background(), util.SessionID, uint64(1))
	err := txn.Commit(ctx)
	s.Require().ErrorContains(err, "txn takes too much time")
	s.Require().True(txn.GetCommitter().IsAsyncCommit())

	expiringOracle.expireLocks.Store(true)
	s.mustPointGet(primary, []byte("v1"))
	s.mustPointGet(secondary, []byte("v2"))

	s.store.SetOracle(baseOracle)
	cleanupTxn, err := s.store.Begin()
	s.Require().NoError(err)
	s.Require().NoError(cleanupTxn.Delete(primary))
	s.Require().NoError(cleanupTxn.Delete(secondary))
	s.Require().NoError(cleanupTxn.Commit(context.Background()))
}

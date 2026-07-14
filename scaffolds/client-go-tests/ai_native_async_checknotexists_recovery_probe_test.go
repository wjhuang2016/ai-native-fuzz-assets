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
	"errors"
	"sync/atomic"
	"time"

	"github.com/pingcap/failpoint"
	tikverr "github.com/tikv/client-go/v2/error"
	"github.com/tikv/client-go/v2/kv"
	"github.com/tikv/client-go/v2/oracle"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/util"
)

type aiNativeProofFailureExpiringOracle struct {
	oracle.Oracle
	expireLocks atomic.Bool
}

func (o *aiNativeProofFailureExpiringOracle) IsExpired(lockTS, ttl uint64, opt *oracle.Option) bool {
	if o.expireLocks.Load() {
		return true
	}
	return o.Oracle.IsExpired(lockTS, ttl, opt)
}

func (s *testAsyncCommitFailSuite) TestAINativeFailedCheckNotExistsMustNotRecoverPrimaryAsCommitted() {
	ctx := context.WithValue(context.Background(), util.SessionID, uint64(1))
	realKey := s.key("a-ai-native-proof-failure-real")
	splitKey := s.key("m-ai-native-proof-failure-split")
	proofKey := s.key("z-ai-native-proof-failure-proof")

	bo := tikv.NewBackofferWithVars(ctx, 5000, nil)
	oldLoc, err := s.store.GetRegionCache().LocateKey(bo, splitKey)
	s.Require().NoError(err)
	_, err = s.store.SplitRegions(ctx, [][]byte{splitKey}, false, nil)
	s.Require().NoError(err)
	s.store.GetRegionCache().InvalidateCachedRegion(oldLoc.Region)
	s.Eventually(func() bool {
		bo := tikv.NewBackofferWithVars(ctx, 5000, nil)
		realLoc, realErr := s.store.GetRegionCache().LocateKey(bo, realKey)
		proofLoc, proofErr := s.store.GetRegionCache().LocateKey(bo, proofKey)
		return realErr == nil && proofErr == nil && realLoc.Region.GetID() != proofLoc.Region.GetID()
	}, 10*time.Second, 50*time.Millisecond)

	cleanupKeys := func() {
		txn := s.begin()
		s.Require().NoError(txn.Delete(realKey))
		s.Require().NoError(txn.Delete(proofKey))
		s.Require().NoError(txn.Commit(context.Background()))
	}
	cleanupKeys()
	s.putKV(proofKey, []byte("existing"), false)

	baseOracle := s.store.GetOracle()
	expiringOracle := &aiNativeProofFailureExpiringOracle{Oracle: baseOracle}
	s.store.SetOracle(expiringOracle)
	defer func() {
		s.store.SetOracle(baseOracle)
		cleanupKeys()
	}()

	s.Require().NoError(failpoint.Enable("tikvclient/prewriteSecondarySleep", "return(200)"))
	s.Require().NoError(failpoint.Enable("tikvclient/commitFailedSkipCleanup", "return"))
	defer func() {
		s.Require().NoError(failpoint.Disable("tikvclient/commitFailedSkipCleanup"))
		s.Require().NoError(failpoint.Disable("tikvclient/prewriteSecondarySleep"))
	}()

	txn := s.beginAsyncCommit()
	s.Require().NoError(txn.Set(realKey, []byte("business-write")))
	s.Require().NoError(txn.GetMemBuffer().SetWithFlags(proofKey, []byte("candidate"), kv.SetPresumeKeyNotExists))
	s.Require().NoError(txn.Delete(proofKey))

	err = txn.Commit(ctx)
	s.Require().Error(err)
	var keyExistErr *tikverr.ErrKeyExist
	s.Require().True(errors.As(err, &keyExistErr), err)
	s.Require().False(tikverr.IsErrorUndetermined(err))
	if txn.GetCommitter().IsAsyncCommit() {
		primaryLock := s.mustGetLock(realKey)
		s.Require().True(primaryLock.UseAsyncCommit)
		resolver := tikv.NewLockResolverProb(s.store.GetLockResolver())
		status, statusErr := resolver.GetTxnStatus(bo, txn.StartTS(), realKey, 0, 0, false, false, nil)
		s.Require().NoError(statusErr)
		s.Require().Empty(resolver.GetSecondariesFromTxnStatus(status))
	}

	expiringOracle.expireLocks.Store(true)
	snapshot := s.store.GetSnapshot(^uint64(0))
	_, err = snapshot.Get(context.Background(), realKey)
	s.Require().ErrorIs(err, tikverr.ErrNotExist)
	s.mustPointGet(proofKey, []byte("existing"))
}

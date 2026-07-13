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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/stretchr/testify/require"
	tikverr "github.com/tikv/client-go/v2/error"
)

const (
	aiNativeOnePCSplitReady = "/tmp/ai-native-1pc-split-ready"
	aiNativeOnePCSplitDone  = "/tmp/ai-native-1pc-split-done"
)

func (s *testOnePCSuite) TestAINativeOnePCLosesAmbiguityAfterRegionFallback() {
	prefix := os.Getenv("AI_NATIVE_1PC_PREFIX")
	if prefix == "" {
		prefix = fmt.Sprintf("ai-native-1pc-%d-", time.Now().UnixNano())
	}
	primary := []byte(prefix + "a")
	secondary := []byte(prefix + "z")
	splitKey := []byte(prefix + "m")

	loc, err := s.store.GetRegionCache().LocateKey(s.bo, primary)
	s.Require().NoError(err)
	secondaryLoc, err := s.store.GetRegionCache().LocateKey(s.bo, secondary)
	s.Require().NoError(err)
	s.Require().Equal(loc.Region.GetID(), secondaryLoc.Region.GetID(), "precondition: both keys must begin in one region")

	s.Require().NoError(failpoint.Enable("tikvclient/mockRetrySendReqToRegion", "1*return(true)->return(false)"))
	defer func() { _ = failpoint.Disable("tikvclient/mockRetrySendReqToRegion") }()
	s.Require().NoError(failpoint.Enable("tikvclient/aiNativeOnePCRememberLostResponse", "1*return(true)->off"))
	defer func() { _ = failpoint.Disable("tikvclient/aiNativeOnePCRememberLostResponse") }()
	s.Require().NoError(failpoint.Enable("tikvclient/invalidCacheAndRetry", "1*off->pause"))
	defer func() { _ = failpoint.Disable("tikvclient/invalidCacheAndRetry") }()
	txn := s.begin1PC()
	s.Require().NoError(txn.Set(primary, []byte("v1")))
	s.Require().NoError(txn.Set(secondary, []byte("v2")))
	defer func() {
		cleanupTxn, cleanupErr := s.store.Begin()
		if cleanupErr != nil {
			return
		}
		_ = cleanupTxn.Delete(primary)
		_ = cleanupTxn.Delete(secondary)
		_ = cleanupTxn.Commit(context.Background())
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- txn.Commit(context.Background())
	}()

	// The second request is paused after the first TryOnePc request has executed
	// but its response has been replaced by an RPC error.
	time.Sleep(time.Second)
	if *withTiKV {
		_ = os.Remove(aiNativeOnePCSplitReady)
		_ = os.Remove(aiNativeOnePCSplitDone)
		s.Require().NoError(os.WriteFile(aiNativeOnePCSplitReady, splitKey, 0o600))
		defer func() {
			_ = os.Remove(aiNativeOnePCSplitReady)
			_ = os.Remove(aiNativeOnePCSplitDone)
		}()
		deadline := time.Now().Add(60 * time.Second)
		for {
			if _, statErr := os.Stat(aiNativeOnePCSplitDone); statErr == nil {
				break
			}
			if time.Now().After(deadline) {
				s.T().Fatal("timed out waiting for external region split")
			}
			time.Sleep(100 * time.Millisecond)
		}
	} else {
		newRegionID := s.cluster.AllocID()
		newPeerID := s.cluster.AllocID()
		s.cluster.Split(loc.Region.GetID(), newRegionID, splitKey, []uint64{newPeerID}, newPeerID)
	}
	s.Require().NoError(failpoint.Disable("tikvclient/invalidCacheAndRetry"))

	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		s.T().Fatal("timed out waiting for 1PC retry")
	}

	if os.Getenv("AI_NATIVE_EXPECT_REAL_TIKV_IDEMPOTENCE") == "1" {
		s.Require().NoError(err)
		s.Require().False(txn.GetCommitter().IsOnePC())
		s.Require().Nil(txn.GetCommitter().GetUndeterminedErr())
		s.Require().Greater(txn.GetCommitter().GetCommitTS(), txn.StartTS())
		s.mustPointGet(primary, []byte("v1"))
		s.mustPointGet(secondary, []byte("v2"))
		return
	}

	s.Require().ErrorContains(err, "write conflict")
	s.Require().False(tikverr.IsErrorUndetermined(err), "the sent TryOnePc request already made the result ambiguous")
	s.Require().False(txn.GetCommitter().IsOnePC())
	s.Require().Nil(txn.GetCommitter().GetUndeterminedErr())
	s.mustPointGet(primary, []byte("v1"))
	s.mustPointGet(secondary, []byte("v2"))
}

func TestAINativeExternalSplitRegion(t *testing.T) {
	if !*withTiKV {
		t.Skip("external splitter is only used for the real TiKV lift")
	}

	splitKey, err := os.ReadFile(aiNativeOnePCSplitReady)
	require.NoError(t, err)
	store := NewTestStore(t)
	defer store.Close()
	_, err = store.SplitRegions(context.Background(), [][]byte{splitKey}, false, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(aiNativeOnePCSplitDone, []byte("done"), 0o600))
}

func TestAINativeCleanupOnePCKeys(t *testing.T) {
	if !*withTiKV {
		t.Skip("cleanup helper is only used for the real TiKV lift")
	}
	prefix := os.Getenv("AI_NATIVE_1PC_PREFIX")
	require.NotEmpty(t, prefix)
	store := NewTestStore(t)
	defer store.Close()
	txn, err := store.Begin()
	require.NoError(t, err)
	require.NoError(t, txn.Delete([]byte(prefix+"a")))
	require.NoError(t, txn.Delete([]byte(prefix+"z")))
	require.NoError(t, txn.Commit(context.Background()))
}

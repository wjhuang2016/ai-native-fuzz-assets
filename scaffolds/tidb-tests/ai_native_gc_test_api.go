//go:build intest

package gcworker

import (
	"context"
	"time"

	"github.com/pingcap/errors"
	"github.com/pingcap/kvproto/pkg/kvrpcpb"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/tikvrpc"
	pd "github.com/tikv/pd/client"
)

func NewWorkerForAINativeTest(store kv.Storage, pdClient pd.Client) (*GCWorker, error) {
	return NewGCWorker(store, pdClient)
}

func (w *GCWorker) PrepareForAINativeTest(ctx context.Context) (bool, uint64, error) {
	return w.prepare(ctx)
}

func (w *GCWorker) RunJobForAINativeTest(ctx context.Context, safePoint uint64) error {
	return w.runGCJob(ctx, safePoint, gcConcurrency{v: 1})
}

func (w *GCWorker) SavedSafePointForAINativeTest() (*time.Time, error) {
	return w.loadTime(gcSafePointKey)
}

func (w *GCWorker) WriteVersionAtForAINativeTest(
	ctx context.Context,
	key, value []byte,
	startTS, commitTS uint64,
) error {
	bo := tikv.NewBackoffer(ctx, gcOneRegionMaxBackoff)
	loc, err := w.tikvStore.GetRegionCache().LocateKey(bo, key)
	if err != nil {
		return errors.Trace(err)
	}
	prewriteResp, err := w.tikvStore.SendReq(
		bo,
		tikvrpc.NewRequest(tikvrpc.CmdPrewrite, &kvrpcpb.PrewriteRequest{
			Mutations: []*kvrpcpb.Mutation{{
				Op: kvrpcpb.Op_Put, Key: key, Value: value,
			}},
			PrimaryLock: key, StartVersion: startTS, LockTtl: 3000, TxnSize: 1,
		}),
		loc.Region,
		tikv.ReadTimeoutMedium,
	)
	if err != nil {
		return errors.Trace(err)
	}
	if regionErr, err := prewriteResp.GetRegionError(); err != nil {
		return errors.Trace(err)
	} else if regionErr != nil {
		return errors.Errorf("prewrite region error: %s", regionErr)
	}
	prewriteBody, ok := prewriteResp.Resp.(*kvrpcpb.PrewriteResponse)
	if !ok {
		return errors.Errorf("unexpected prewrite response %T", prewriteResp.Resp)
	}
	if len(prewriteBody.Errors) != 0 {
		return errors.Errorf("prewrite key error: %s", prewriteBody.Errors[0])
	}
	commitResp, err := w.tikvStore.SendReq(
		bo,
		tikvrpc.NewRequest(tikvrpc.CmdCommit, &kvrpcpb.CommitRequest{
			StartVersion: startTS, Keys: [][]byte{key}, PrimaryKey: key,
			CommitVersion: commitTS, CommitRole: kvrpcpb.CommitRole_Primary,
		}),
		loc.Region,
		tikv.ReadTimeoutMedium,
	)
	if err != nil {
		return errors.Trace(err)
	}
	if regionErr, err := commitResp.GetRegionError(); err != nil {
		return errors.Trace(err)
	} else if regionErr != nil {
		return errors.Errorf("commit region error: %s", regionErr)
	}
	commitBody, ok := commitResp.Resp.(*kvrpcpb.CommitResponse)
	if !ok {
		return errors.Errorf("unexpected commit response %T", commitResp.Resp)
	}
	if commitBody.Error != nil {
		return errors.Errorf("commit key error: %s", commitBody.Error)
	}
	return nil
}

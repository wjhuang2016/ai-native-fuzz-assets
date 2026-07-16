// Test-only utility for advancing GCV2 safe points on an isolated cluster.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/pingcap/kvproto/pkg/pdpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("pd", "127.0.0.1:2379", "PD gRPC endpoint")
	safePoint := flag.Uint64("safe-point", 0, "safe point TSO; defaults to current physical time")
	flag.Parse()
	if *safePoint == 0 {
		*safePoint = uint64(time.Now().UnixMilli()) << 18
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := pdpb.NewPDClient(conn)

	members, err := client.GetMembers(ctx, &pdpb.GetMembersRequest{})
	if err != nil {
		log.Fatal(err)
	}
	if err := responseError(members.GetHeader()); err != nil {
		log.Fatal(err)
	}
	header := &pdpb.RequestHeader{ClusterId: members.GetHeader().GetClusterId()}
	scope := &pdpb.KeyspaceScope{KeyspaceId: math.MaxUint32}

	txnResult, err := client.AdvanceTxnSafePoint(ctx, &pdpb.AdvanceTxnSafePointRequest{
		Header: header, KeyspaceScope: scope, Target: *safePoint,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := responseError(txnResult.GetHeader()); err != nil {
		log.Fatal(err)
	}
	if txnResult.GetNewTxnSafePoint() != *safePoint {
		log.Fatalf("txn safe point blocked: old=%d requested=%d updated=%d blocker=%q",
			txnResult.GetOldTxnSafePoint(), *safePoint, txnResult.GetNewTxnSafePoint(),
			txnResult.GetBlockerDescription())
	}

	gcResult, err := client.AdvanceGCSafePoint(ctx, &pdpb.AdvanceGCSafePointRequest{
		Header: header, KeyspaceScope: scope, Target: *safePoint,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := responseError(gcResult.GetHeader()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("txn_old=%d requested=%d txn_updated=%d gc_old=%d gc_updated=%d\n",
		txnResult.GetOldTxnSafePoint(), *safePoint, txnResult.GetNewTxnSafePoint(),
		gcResult.GetOldGcSafePoint(), gcResult.GetNewGcSafePoint())
}

func responseError(header *pdpb.ResponseHeader) error {
	if header == nil {
		return fmt.Errorf("missing PD response header")
	}
	if header.GetError() != nil {
		return fmt.Errorf("PD error: %s", header.GetError().GetMessage())
	}
	return nil
}

// Test-only utility for advancing the global GC safe point on an isolated cluster.
// Build this inside the matching PD repository's tools module.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	pd "github.com/tikv/pd/client"
	"github.com/tikv/pd/client/pkg/caller"
)

func main() {
	addr := flag.String("pd", "127.0.0.1:2379", "PD endpoint")
	safePoint := flag.Uint64("safe-point", 0, "GC safe point TSO; defaults to current physical time")
	flag.Parse()

	if *safePoint == 0 {
		*safePoint = uint64(time.Now().UnixMilli()) << 18
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := pd.NewClientWithContext(
		ctx,
		caller.TestComponent,
		[]string{*addr},
		pd.SecurityOption{},
	)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	old, err := client.UpdateGCSafePoint(ctx, 0)
	if err != nil {
		log.Fatal(err)
	}
	updated, err := client.UpdateGCSafePoint(ctx, *safePoint)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("old=%d requested=%d updated=%d\n", old, *safePoint, updated)
}

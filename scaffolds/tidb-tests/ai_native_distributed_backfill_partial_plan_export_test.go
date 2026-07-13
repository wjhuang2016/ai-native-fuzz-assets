package ddl

import (
	"context"

	"github.com/pingcap/tidb/pkg/kv"
)

// SetAllocNewTSForBackfillForTest replaces the planner's TSO allocator for a
// single test. Apply the companion source-seam patch before using this helper.
func SetAllocNewTSForBackfillForTest(
	fn func(context.Context, kv.StorageWithPD) (uint64, error),
) func() {
	old := allocNewTSForBackfill
	allocNewTSForBackfill = fn
	return func() {
		allocNewTSForBackfill = old
	}
}

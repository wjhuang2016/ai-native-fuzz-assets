package runaway

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchFlusherRetriesFailedBatch(t *testing.T) {
	var attempts atomic.Int32
	persisted := make(map[string]int)
	flusher := newTestBatchFlusher(
		100,
		func(m map[string]int, k string, v int) { m[k] = v },
		func(m map[string]int) error {
			if attempts.Add(1) == 1 {
				return errors.New("transient write failure")
			}
			for k, v := range m {
				persisted[k] = v
			}
			return nil
		},
	)

	flusher.add("kill-rule-for-digest", 1)
	flusher.flush()
	flusher.flush()

	require.Equal(t, int32(2), attempts.Load())
	require.Equal(t, map[string]int{"kill-rule-for-digest": 1}, persisted)
}

package brregistrytest

import (
	"testing"

	"github.com/pingcap/tidb/tests/realtikvtest"
)

func TestMain(m *testing.M) {
	realtikvtest.RunTestMain(m)
}

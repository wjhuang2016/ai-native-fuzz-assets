package ddl

import (
	"database/sql/driver"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/go-sql-driver/mysql"
	perrors "github.com/pingcap/errors"
	"github.com/pingcap/tidb/pkg/ingestor/errdef"
	"github.com/pingcap/tidb/pkg/ingestor/ingestcli"
	lcommon "github.com/pingcap/tidb/pkg/lightning/common"
	"github.com/stretchr/testify/require"
	pderrors "github.com/tikv/pd/client/errs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAINativePDTSOErrorRetryClassificationProbe(t *testing.T) {
	raw := pderrors.ErrClientCreateTSOStream.FastGenByArgs(pderrors.RetryTimeoutErr)
	traced := perrors.Trace(raw)
	stacked := perrors.WithStack(raw)
	synthesized := toTError(raw)

	t.Logf("raw type=%T cause=%T retryable=%v msg=%v", raw, perrors.Cause(raw), isRetryableError(raw, true), raw)
	t.Logf("traced type=%T cause=%T retryable=%v msg=%v", traced, perrors.Cause(traced), isRetryableError(traced, true), traced)
	t.Logf("stacked type=%T cause=%T retryable=%v msg=%v", stacked, perrors.Cause(stacked), isRetryableError(stacked, true), stacked)
	t.Logf("synthesized type=%T cause=%T retryable=%v msg=%v", synthesized, perrors.Cause(synthesized), isRetryableError(synthesized, true), synthesized)
}

// This is a methodology probe, not product coverage: we want a compact map of
// foreign/transient error shapes that stay retryable in other recovery code but
// turn non-retryable once DDL bridges them through toTError/terror.
func TestAINativeRetryClassifierGapProbe(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantDDLRaw    bool
		wantLightning bool
	}{
		{
			name:          "pd_tso_retry_timeout",
			err:           pderrors.ErrClientCreateTSOStream.FastGenByArgs(pderrors.RetryTimeoutErr),
			wantDDLRaw:    false,
			wantLightning: false,
		},
		{
			name:          "ingest_kv_not_leader",
			err:           &ingestcli.IngestAPIError{Err: errdef.ErrKVNotLeader.GenWithStack("mock ingest not leader")},
			wantDDLRaw:    false,
			wantLightning: true,
		},
		{
			name:          "ingest_kv_region_not_found",
			err:           &ingestcli.IngestAPIError{Err: errdef.ErrKVRegionNotFound.GenWithStack("mock ingest region not found")},
			wantDDLRaw:    false,
			wantLightning: true,
		},
		{
			name:          "ingest_kv_no_leader",
			err:           &ingestcli.IngestAPIError{Err: errdef.ErrNoLeader.GenWithStackByArgs(42)},
			wantDDLRaw:    false,
			wantLightning: true,
		},
		{
			name:          "grpc_unavailable",
			err:           status.Error(codes.Unavailable, "transport is closing"),
			wantDDLRaw:    true,
			wantLightning: true,
		},
		{
			name:          "grpc_dataloss",
			err:           status.Error(codes.DataLoss, "stream terminated"),
			wantDDLRaw:    true,
			wantLightning: true,
		},
		{
			name:          "mysql_invalid_conn",
			err:           mysql.ErrInvalidConn,
			wantDDLRaw:    true,
			wantLightning: true,
		},
		{
			name:          "driver_bad_conn",
			err:           driver.ErrBadConn,
			wantDDLRaw:    true,
			wantLightning: true,
		},
		{
			name:          "net_conn_reset",
			wantDDLRaw:    true,
			wantLightning: true,
			err: &net.OpError{
				Op:  "read",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET},
			},
		},
		{
			name:          "net_broken_pipe",
			wantDDLRaw:    true,
			wantLightning: true,
			err: &net.OpError{
				Op:  "write",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE},
			},
		},
		{
			name:          "net_conn_refused",
			wantDDLRaw:    true,
			wantLightning: true,
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawRetryable := isRetryableError(tc.err, true)
			tracedRetryable := isRetryableError(perrors.Trace(tc.err), true)
			stackedRetryable := isRetryableError(perrors.WithStack(tc.err), true)
			ddlSynthRetryable := isRetryableError(toTError(tc.err), true)
			lightningRetryable := lcommon.IsRetryableError(tc.err)
			require.Equal(t, tc.wantDDLRaw, rawRetryable)
			require.Equal(t, tc.wantDDLRaw, tracedRetryable)
			require.Equal(t, tc.wantDDLRaw, stackedRetryable)
			require.Equal(t, tc.wantLightning, lightningRetryable)

			t.Logf(
				"case=%s raw=%v traced=%v stacked=%v ddl_synth=%v lightning=%v err=%T %v",
				tc.name,
				rawRetryable,
				tracedRetryable,
				stackedRetryable,
				ddlSynthRetryable,
				lightningRetryable,
				tc.err,
				tc.err,
			)
		})
	}
}

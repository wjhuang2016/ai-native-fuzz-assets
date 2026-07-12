// Copy into pkg/executor/importer/importer_testkit_test.go. It reuses that file's test helpers.
package importer_test

import (
	"context"
	"errors"
	"os"
	"path"
	"testing"

	"github.com/pingcap/tidb/br/pkg/mock"
	tidb "github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/executor/importer"
	"github.com/pingcap/tidb/pkg/lightning/backend"
	"github.com/pingcap/tidb/pkg/lightning/common"
	"github.com/pingcap/tidb/pkg/lightning/mydump"
	verify "github.com/pingcap/tidb/pkg/lightning/verification"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestProcessChunkPropagatesWriterCloseErrors(t *testing.T) {
	ctx := context.Background()
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table t(a int, b int, c int, index idx_b(b))")

	fileName := path.Join(tidb.GetGlobalConfig().TempDir, "ai-native-close-error.csv")
	sourceData := []byte("1,2,3\n4,5,6\n")
	require.NoError(t, os.WriteFile(fileName, sourceData, 0o644))
	t.Cleanup(func() { require.NoError(t, os.Remove(fileName)) })

	ti := getTableImporter(ctx, t, store, "t", fileName, importer.DataFormatCSV, nil)
	t.Cleanup(func() {
		ti.LoadDataController.Close()
		ti.Backend().CloseEngineMgr()
	})
	chunkInfo := &importer.Chunk{
		Path: "ai-native-close-error.csv", Type: mydump.SourceTypeCSV,
		EndOffset: int64(len(sourceData)), RowIDMax: 10000,
	}

	ctrl := gomock.NewController(t)
	mockBackend := mock.NewMockBackend(ctrl)
	engineMgr := backend.MakeEngineManager(mockBackend)
	engineCfg := &backend.EngineConfig{}
	mockBackend.EXPECT().OpenEngine(gomock.Any(), engineCfg, gomock.Any()).Return(nil).Times(2)
	dataEngine, err := engineMgr.OpenEngine(ctx, engineCfg, "`test`.`t`", 1)
	require.NoError(t, err)
	indexEngine, err := engineMgr.OpenEngine(ctx, engineCfg, "`test`.`t`", common.IndexEngineID)
	require.NoError(t, err)

	dataWriter := mock.NewMockEngineWriter(ctrl)
	indexWriter := mock.NewMockEngineWriter(ctrl)
	gomock.InOrder(
		mockBackend.EXPECT().LocalWriter(gomock.Any(), gomock.Any(), gomock.Any()).Return(dataWriter, nil),
		mockBackend.EXPECT().LocalWriter(gomock.Any(), gomock.Any(), gomock.Any()).Return(indexWriter, nil),
	)
	dataWriter.EXPECT().AppendRows(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	indexWriter.EXPECT().AppendRows(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	indexWriter.EXPECT().Close(gomock.Any()).Return(nil, errors.New("ai-native index writer close failed"))
	dataWriter.EXPECT().Close(gomock.Any()).Return(nil, errors.New("ai-native data writer close failed"))

	checksum := verify.NewKVGroupChecksumWithKeyspace(store.GetCodec().GetKeyspace())
	err = importer.ProcessChunk(ctx, chunkInfo, ti, dataEngine, indexEngine, zap.NewNop(), checksum, nil)
	require.ErrorContains(t, err, "ai-native data writer close failed")
	require.ErrorContains(t, err, "ai-native index writer close failed")
}

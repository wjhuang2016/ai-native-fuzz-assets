// Copyright 2026 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpoints_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pingcap/tidb/lightning/pkg/checkpoints"
	"github.com/pingcap/tidb/pkg/lightning/common"
	"github.com/pingcap/tidb/pkg/lightning/config"
	"github.com/pingcap/tidb/pkg/lightning/importdef"
	"github.com/pingcap/tidb/pkg/lightning/mydump"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/stretchr/testify/require"
)

func TestAINativeFileCheckpointRejectsTableGenerationReuse(t *testing.T) {
	ctx := context.Background()
	tableName := common.UniqueTable("db", "t")

	newConfig := func() *config.Config {
		cfg := config.NewConfig()
		cfg.TaskID = 1001
		cfg.Mydumper.SourceDir = "/same/source/path"
		cfg.TikvImporter.Backend = config.BackendLocal
		cfg.TikvImporter.AddIndexBySQL = true
		return cfg
	}
	newDBInfo := func(tableID int64, columnName string) map[string]*importdef.DBInfo {
		tableInfo := &model.TableInfo{
			ID:   tableID,
			Name: ast.NewCIStr("t"),
			Columns: []*model.ColumnInfo{{
				ID:   1,
				Name: ast.NewCIStr(columnName),
			}},
		}
		return map[string]*importdef.DBInfo{
			"db": {
				ID:   1,
				Name: "db",
				Tables: map[string]*importdef.TableInfo{
					"t": {
						ID:      tableID,
						DB:      "db",
						Name:    "t",
						Core:    tableInfo,
						Desired: tableInfo,
					},
				},
			},
		}
	}

	seedCompletedGeneration := func(t *testing.T, path string) {
		cpdb, err := checkpoints.NewFileCheckpointsDB(ctx, path)
		require.NoError(t, err)
		require.NoError(t, cpdb.Initialize(ctx, newConfig(), newDBInfo(101, "old_col")))
		require.NoError(t, cpdb.InsertEngineCheckpoints(ctx, tableName, map[int32]*checkpoints.EngineCheckpoint{
			0: {
				Chunks: []*checkpoints.ChunkCheckpoint{{
					Key:      checkpoints.ChunkCheckpointKey{Path: "/same/source/path/old.csv"},
					FileMeta: mydump.SourceFileMeta{Path: "/same/source/path/old.csv", FileSize: 100},
					Chunk:    mydump.Chunk{EndOffset: 100},
				}},
			},
			common.IndexEngineID: {},
		}))

		diff := checkpoints.NewTableCheckpointDiff()
		(&checkpoints.StatusCheckpointMerger{EngineID: 0, Status: checkpoints.CheckpointStatusImported}).MergeInto(diff)
		(&checkpoints.StatusCheckpointMerger{EngineID: common.IndexEngineID, Status: checkpoints.CheckpointStatusImported}).MergeInto(diff)
		(&checkpoints.StatusCheckpointMerger{EngineID: checkpoints.WholeTableEngineID, Status: checkpoints.CheckpointStatusAnalyzed}).MergeInto(diff)
		require.NoError(t, cpdb.Update(ctx, map[string]*checkpoints.TableCheckpointDiff{tableName: diff}))
		require.NoError(t, cpdb.Close())
	}

	t.Run("same generation resumes completed work", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.pb")
		seedCompletedGeneration(t, path)
		cpdb, err := checkpoints.NewFileCheckpointsDB(ctx, path)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, cpdb.Close()) })
		require.NoError(t, cpdb.Initialize(ctx, newConfig(), newDBInfo(101, "old_col")))
		cp, err := cpdb.Get(ctx, tableName)
		require.NoError(t, err)
		require.Equal(t, int64(101), cp.TableID)
		require.Equal(t, checkpoints.CheckpointStatusAnalyzed, cp.Status)
		require.Len(t, cp.Engines, 2)
	})

	t.Run("fresh current generation starts loaded", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.pb")
		cpdb, err := checkpoints.NewFileCheckpointsDB(ctx, path)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, cpdb.Close()) })
		require.NoError(t, cpdb.Initialize(ctx, newConfig(), newDBInfo(202, "new_col")))
		cp, err := cpdb.Get(ctx, tableName)
		require.NoError(t, err)
		require.Equal(t, int64(202), cp.TableID)
		require.Equal(t, checkpoints.CheckpointStatusLoaded, cp.Status)
		require.Empty(t, cp.Engines)
	})

	t.Run("same name new generation cannot inherit completion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.pb")
		seedCompletedGeneration(t, path)
		cpdb, err := checkpoints.NewFileCheckpointsDB(ctx, path)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, cpdb.Close()) })

		if err := cpdb.Initialize(ctx, newConfig(), newDBInfo(202, "new_col")); err != nil {
			return
		}
		cp, err := cpdb.Get(ctx, tableName)
		require.NoError(t, err)
		actual := struct {
			TableID int64
			Status  checkpoints.CheckpointStatus
			Engines int
		}{cp.TableID, cp.Status, len(cp.Engines)}
		expected := struct {
			TableID int64
			Status  checkpoints.CheckpointStatus
			Engines int
		}{202, checkpoints.CheckpointStatusLoaded, 0}
		require.Equal(t, expected, actual)
	})
}

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
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package importintotest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"go.uber.org/atomic"
)

func (s *mockGCSSuite) TestAINativeImportIntoAddIndexAfterTargetResolution() {
	if kerneltype.IsNextGen() {
		s.T().Skip("classic kernel table-mode race probe")
	}

	dataPath := filepath.Join(s.T().TempDir(), "data.csv")
	s.Require().NoError(os.WriteFile(dataPath, []byte("1,101\n2,102\n3,103\n"), 0o600))
	s.prepareAndUseDB("ai_native_schema_race")
	s.tk.MustExec("create table t (id bigint primary key, v bigint not null)")

	tkDDL := testkit.NewTestKit(s.T(), s.store)
	tkDDL.MustExec("use ai_native_schema_race")
	var fired atomic.Bool
	testfailpoint.EnableCall(s.T(), "github.com/pingcap/tidb/pkg/executor/importer/NewImportPlan",
		func(_ any) {
			if fired.CompareAndSwap(false, true) {
				tkDDL.MustExec("alter table t add unique index kv(v)")
			}
		},
	)

	importSQL := fmt.Sprintf("import into t from '%s'", dataPath)
	result := s.tk.MustQuery(importSQL).Rows()
	s.Require().True(fired.Load())
	s.Require().Equal("finished", result[0][fmap["Status"]])

	s.tk.MustQuery("select id, v from t use index() order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103"))
	s.tk.MustQuery("select id, v from t force index(kv) where v >= 0 order by id").
		Check(testkit.Rows())
	s.tk.MustExec("insert into t values (4, 101)")
	s.tk.MustQuery("select id, v from t use index() order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103", "4 101"))
	s.Require().Error(s.tk.ExecToErr("admin check table t"))
}

func (s *mockGCSSuite) TestAINativeImportIntoAddIndexDuringNaturalFileDiscovery() {
	if kerneltype.IsNextGen() {
		s.T().Skip("classic kernel table-mode race probe")
	}

	dataDir := s.T().TempDir()
	dataPath := filepath.Join(dataDir, "data-000.csv")
	s.Require().NoError(os.WriteFile(dataPath, []byte("1,101\n2,102\n3,103\n"), 0o600))
	for i := range 60_000 {
		noisePath := filepath.Join(dataDir, fmt.Sprintf("noise-%05d.tmp", i))
		f, err := os.Create(noisePath)
		s.Require().NoError(err)
		s.Require().NoError(f.Close())
	}

	s.prepareAndUseDB("ai_native_schema_natural_race")
	s.tk.MustExec("create table t (id bigint primary key, v bigint not null)")

	tkImport := testkit.NewTestKit(s.T(), s.store)
	tkImport.MustExec("use ai_native_schema_natural_race")
	tkDDL := testkit.NewTestKit(s.T(), s.store)
	tkDDL.MustExec("use ai_native_schema_natural_race")

	importSQL := fmt.Sprintf("import into t from '%s'", filepath.Join(dataDir, "data-*.csv"))
	importDone := make(chan error, 1)
	importStarted := make(chan struct{})
	go func() {
		close(importStarted)
		importDone <- tkImport.QueryToErr(importSQL)
	}()

	<-importStarted
	time.Sleep(250 * time.Millisecond)

	tkDDL.MustExec("alter table t add unique index kv(v)")

	select {
	case err := <-importDone:
		s.Require().NoError(err)
	case <-time.After(2 * time.Minute):
		s.T().Fatal("IMPORT INTO did not finish")
	}

	rows := s.tk.MustQuery("show import jobs").Rows()
	foundFinishedJob := false
	for _, row := range rows {
		if row[fmap["TargetTable"]] == "`ai_native_schema_natural_race`.`t`" {
			s.Require().Equal("finished", strings.ToLower(row[fmap["Status"]].(string)))
			foundFinishedJob = true
			break
		}
	}
	s.Require().True(foundFinishedJob)
	s.tk.MustQuery("select id, v from t use index() order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103"))
	s.tk.MustQuery("select id, v from t force index(kv) where v >= 0 order by id").
		Check(testkit.Rows())
	s.tk.MustExec("insert into t values (4, 101)")
	s.tk.MustQuery("select id, v from t use index() order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103", "4 101"))
	s.Require().Error(s.tk.ExecToErr("admin check table t"))
}

func (s *mockGCSSuite) TestAINativeImportIntoAddIndexBeforePlanningControl() {
	if kerneltype.IsNextGen() {
		s.T().Skip("classic kernel table-mode control")
	}

	dataPath := filepath.Join(s.T().TempDir(), "data.csv")
	s.Require().NoError(os.WriteFile(dataPath, []byte("1,101\n2,102\n3,103\n"), 0o600))
	s.prepareAndUseDB("ai_native_schema_control")
	s.tk.MustExec("create table t (id bigint primary key, v bigint not null)")
	s.tk.MustExec("alter table t add unique index kv(v)")

	result := s.tk.MustQuery(fmt.Sprintf("import into t from '%s'", dataPath)).Rows()
	s.Require().Equal("finished", result[0][fmap["Status"]])
	s.tk.MustQuery("select id, v from t use index() order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103"))
	s.tk.MustQuery("select id, v from t force index(kv) where v >= 0 order by id").
		Check(testkit.Rows("1 101", "2 102", "3 103"))
	s.Require().Error(s.tk.ExecToErr("insert into t values (4, 101)"))
	s.tk.MustExec("admin check table t")
}

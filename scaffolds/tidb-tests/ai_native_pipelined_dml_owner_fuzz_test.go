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

//go:build !nextgen

package pipelineddmltest

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativePipelinedDMLCrossGenerationOwnerFuzz(t *testing.T) {
	require.NoError(t, failpoint.Enable("tikvclient/pipelinedMemDBMinFlushKeys", `return(10)`))
	require.NoError(t, failpoint.Enable("tikvclient/pipelinedMemDBMinFlushSize", `return(100)`))
	require.NoError(t, failpoint.Enable("tikvclient/pipelinedMemDBForceFlushSizeThreshold", `return(4096)`))
	defer func() {
		require.NoError(t, failpoint.Disable("tikvclient/pipelinedMemDBMinFlushKeys"))
		require.NoError(t, failpoint.Disable("tikvclient/pipelinedMemDBMinFlushSize"))
		require.NoError(t, failpoint.Disable("tikvclient/pipelinedMemDBForceFlushSizeThreshold"))
	}()

	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	fresh := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	fresh.MustExec("use test")
	tk.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))

	createTable := func(name string) {
		tk.MustExec(fmt.Sprintf(`create table %s(
			id int primary key,
			email int not null,
			phone int not null,
			payload int not null,
			g int as (payload %% 13) stored,
			unique key uk_email(email),
			unique key uk_phone(phone),
			key idx_g(g))`, name))
	}
	createSource := func(name string, count int, row func(int) (int, int, int, int)) {
		tk.MustExec(fmt.Sprintf(`create table %s(
			seq int primary key,
			id int not null,
			email int not null,
			phone int not null,
			payload int not null)`, name))
		var sql bytes.Buffer
		_, _ = fmt.Fprintf(&sql, "insert into %s values ", name)
		for i := 0; i < count; i++ {
			if i > 0 {
				sql.WriteByte(',')
			}
			id, email, phone, payload := row(i)
			_, _ = fmt.Fprintf(&sql, "(%d,%d,%d,%d,%d)", i, id, email, phone, payload)
		}
		tk.MustExec(sql.String())
	}
	assertBulk := func() {
		for _, warning := range tk.Session().GetSessionVars().StmtCtx.GetWarnings() {
			require.NotContains(t, warning.Err.Error(), "Fallback to standard mode")
		}
	}
	compare := func(standard, bulk string) {
		queries := []string{
			"select id,email,phone,payload,g from %s order by id",
			"select id,email,phone,payload,g from %s force index(uk_email) order by email,id",
			"select id,email,phone,payload,g from %s force index(uk_phone) order by phone,id",
			"select id,email,phone,payload,g from %s force index(idx_g) order by g,id",
		}
		for _, query := range queries {
			expected := fresh.MustQuery(fmt.Sprintf(query, standard)).Rows()
			actual := fresh.MustQuery(fmt.Sprintf(query, bulk)).Rows()
			require.Equal(t, expected, actual, query)
		}
		fresh.MustExec("admin check table " + standard)
		fresh.MustExec("admin check table " + bulk)
	}

	t.Run("replace-retargets-two-unique-owners", func(t *testing.T) {
		createTable("ai_owner_replace_std")
		createTable("ai_owner_replace_bulk")
		rows := func(i int) (int, int, int, int) {
			return (i * 17) % 31, 1000 + (i*7+i/5)%37, 2000 + (i*11+i/3)%41, i
		}
		createSource("ai_owner_replace_src", 2400, rows)
		tk.MustExec("set session tidb_dml_type = standard")
		tk.MustExec(`replace into ai_owner_replace_std(id,email,phone,payload)
			select id,email,phone,payload from ai_owner_replace_src order by seq`)
		tk.MustExec("set session tidb_dml_type = bulk")
		tk.MustExec(`replace into ai_owner_replace_bulk(id,email,phone,payload)
			select id,email,phone,payload from ai_owner_replace_src order by seq`)
		assertBulk()
		compare("ai_owner_replace_std", "ai_owner_replace_bulk")
	})

	t.Run("on-duplicate-retargets-primary-owner", func(t *testing.T) {
		createTable("ai_owner_ondup_std")
		createTable("ai_owner_ondup_bulk")
		rows := func(i int) (int, int, int, int) {
			return i + 1, 5000 + i%17, 6000 + i, i
		}
		createSource("ai_owner_ondup_src", 2400, rows)
		suffix := " on duplicate key update id=values(id),phone=values(phone),payload=values(payload)"
		tk.MustExec("set session tidb_dml_type = standard")
		tk.MustExec(`insert into ai_owner_ondup_std(id,email,phone,payload)
			select id,email,phone,payload from ai_owner_ondup_src order by seq` + suffix)
		tk.MustExec("set session tidb_dml_type = bulk")
		tk.MustExec(`insert into ai_owner_ondup_bulk(id,email,phone,payload)
			select id,email,phone,payload from ai_owner_ondup_src order by seq` + suffix)
		assertBulk()
		compare("ai_owner_ondup_std", "ai_owner_ondup_bulk")
	})

	t.Run("replace-retargets-common-handle-and-prefix-owners", func(t *testing.T) {
		create := func(name string) {
			tk.MustExec(fmt.Sprintf(`create table %s(
				tenant int not null,
				code varchar(24) collate utf8mb4_general_ci not null,
				email varchar(32) collate utf8mb4_bin,
				payload varchar(32) not null,
				primary key(tenant, code) clustered,
				unique key uk_email(email),
				key idx_payload(payload(6)))`, name))
		}
		create("ai_owner_common_std")
		create("ai_owner_common_bulk")
		tk.MustExec(`create table ai_owner_common_src(
			seq int primary key,
			tenant int not null,
			code varchar(24) not null,
			email varchar(32),
			payload varchar(32) not null)`)
		var sql bytes.Buffer
		sql.WriteString("insert into ai_owner_common_src values ")
		for i := 0; i < 2400; i++ {
			if i > 0 {
				sql.WriteByte(',')
			}
			tenant := i % 7
			code := fmt.Sprintf("Code-%02d-%02d", (i*11)%43, (i/17)%5)
			email := "null"
			if i%4 != 0 {
				email = fmt.Sprintf("'owner-%02d@example.com'", (i*13+i/9)%59)
			}
			_, _ = fmt.Fprintf(&sql, "(%d,%d,'%s',%s,'bucket-%02d-version-%04d')",
				i, tenant, code, email, (i*5)%31, i)
		}
		tk.MustExec(sql.String())
		stmt := `replace into %s(tenant,code,email,payload)
			select tenant,code,email,payload from ai_owner_common_src order by seq`
		tk.MustExec("set session tidb_dml_type = standard")
		tk.MustExec(fmt.Sprintf(stmt, "ai_owner_common_std"))
		tk.MustExec("set session tidb_dml_type = bulk")
		tk.MustExec(fmt.Sprintf(stmt, "ai_owner_common_bulk"))
		assertBulk()
		for _, query := range []string{
			"select tenant,code,email,payload from %s order by tenant,code",
			"select tenant,code,email,payload from %s force index(uk_email) order by email,tenant,code",
			"select tenant,code,email,payload from %s force index(idx_payload) order by payload,tenant,code",
		} {
			require.Equal(t,
				fresh.MustQuery(fmt.Sprintf(query, "ai_owner_common_std")).Rows(),
				fresh.MustQuery(fmt.Sprintf(query, "ai_owner_common_bulk")).Rows(), query)
		}
		fresh.MustExec("admin check table ai_owner_common_std")
		fresh.MustExec("admin check table ai_owner_common_bulk")
	})

	t.Run("replace-rewrites-multi-valued-index-membership", func(t *testing.T) {
		create := func(name string) {
			tk.MustExec(fmt.Sprintf(`create table %s(
				id int primary key,
				owner int not null,
				tags json not null,
				unique key uk_owner(owner),
				key idx_tags((cast(tags as unsigned array))))`, name))
		}
		create("ai_owner_mvi_std")
		create("ai_owner_mvi_bulk")
		tk.MustExec(`create table ai_owner_mvi_src(
			seq int primary key,
			id int not null,
			owner int not null,
			tags json not null)`)
		var sql bytes.Buffer
		sql.WriteString("insert into ai_owner_mvi_src values ")
		for i := 0; i < 1800; i++ {
			if i > 0 {
				sql.WriteByte(',')
			}
			_, _ = fmt.Fprintf(&sql, "(%d,%d,%d,json_array(%d,%d,%d))",
				i, (i*17)%67, 1000+(i*19+i/7)%71, i%23, (i*7)%23, (i/5)%23)
		}
		tk.MustExec(sql.String())
		stmt := `replace into %s(id,owner,tags)
			select id,owner,tags from ai_owner_mvi_src order by seq`
		tk.MustExec("set session tidb_dml_type = standard")
		tk.MustExec(fmt.Sprintf(stmt, "ai_owner_mvi_std"))
		tk.MustExec("set session tidb_dml_type = bulk")
		tk.MustExec(fmt.Sprintf(stmt, "ai_owner_mvi_bulk"))
		assertBulk()
		require.Equal(t,
			fresh.MustQuery("select id,owner,tags from ai_owner_mvi_std order by id").Rows(),
			fresh.MustQuery("select id,owner,tags from ai_owner_mvi_bulk order by id").Rows())
		for tag := 0; tag < 23; tag++ {
			query := "select id,owner from %s where %d member of(tags) order by id"
			require.Equal(t,
				fresh.MustQuery(fmt.Sprintf(query, "ai_owner_mvi_std", tag)).Rows(),
				fresh.MustQuery(fmt.Sprintf(query, "ai_owner_mvi_bulk", tag)).Rows(), "tag=%d", tag)
		}
		fresh.MustExec("admin check table ai_owner_mvi_std")
		fresh.MustExec("admin check table ai_owner_mvi_bulk")
	})

	t.Run("update-preserves-untouched-index-storage", func(t *testing.T) {
		createTable("ai_owner_untouched_std")
		createTable("ai_owner_untouched_bulk")
		var sql bytes.Buffer
		sql.WriteString("insert into ai_owner_untouched_std(id,email,phone,payload) values ")
		for i := 0; i < 2400; i++ {
			if i > 0 {
				sql.WriteByte(',')
			}
			_, _ = fmt.Fprintf(&sql, "(%d,%d,%d,%d)", i, 10000+i, 20000+i, i)
		}
		tk.MustExec(sql.String())
		tk.MustExec(`insert into ai_owner_untouched_bulk(id,email,phone,payload)
			select id,email,phone,payload from ai_owner_untouched_std`)

		tk.MustExec("set session tidb_dml_type = standard")
		tk.MustExec("update ai_owner_untouched_std set payload=payload+13")
		tk.MustExec("set session tidb_dml_type = bulk")
		tk.MustExec("update ai_owner_untouched_bulk set payload=payload+13")
		assertBulk()
		compare("ai_owner_untouched_std", "ai_owner_untouched_bulk")
	})
}

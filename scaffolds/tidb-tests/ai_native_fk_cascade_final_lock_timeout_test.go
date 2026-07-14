package fk_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAIFKCascadeFinalLockTimeoutStatementAtomicity(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	setup.MustExec("use test")
	setup.MustExec("set global tidb_enable_foreign_key = on")
	setup.MustExec("create table ai_parent (id int primary key)")
	setup.MustExec("create table ai_child (id int primary key, pid int, " +
		"constraint ai_fk foreign key (pid) references ai_parent(id) on update cascade)")
	setup.MustExec("create table ai_guard (id int primary key, version int not null)")
	setup.MustExec("insert into ai_parent values (1)")
	setup.MustExec("insert into ai_child values (10, 1)")
	setup.MustExec("insert into ai_guard values (1, 0)")

	holder := testkit.NewTestKit(t, store)
	holder.MustExec("use test")
	holder.MustExec("begin pessimistic")
	holder.MustExec("update ai_guard set version = version + 1 where id = 1")
	defer holder.MustExec("rollback")

	writer := testkit.NewTestKit(t, store)
	writer.MustExec("use test")
	writer.MustQuery("select @@tidb_enable_metadata_lock, @@innodb_lock_wait_timeout, " +
		"@@tidb_constraint_check_in_place_pessimistic, @@global.tidb_enable_foreign_key").
		Check(testkit.Rows("1 50 1 1"))
	writer.MustExec("begin pessimistic")
	err := writer.ExecToErr("update ai_parent as p join ai_guard as g on g.id = 1 " +
		"set p.id = 2, g.version = g.version where p.id = 1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Lock wait timeout")
	writer.MustExec("commit")

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select (select id from ai_parent), (select pid from ai_child where id = 10)").
		Check(testkit.Rows("1 1"))
}

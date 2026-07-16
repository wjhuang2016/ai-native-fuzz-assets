-- Runtime RED on d573e284da773c820c1c313105b73d587378381b; source root still present
-- on GitHub master 94b834d94b604b1940ecc2c3064168337863269d.
INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2760003,
 'Lazy server cursor can lose a stale snapshot to GC and fail during fetch',
 'moderate',
 'transaction/read-availability',
 NULL,
 'lazy cursor fetch / stale read / GC',
 'A server-side stale-read cursor remains open, but TiDB reports a minStartTS newer than its snapshot; a later Region fetch returns Error 9006.',
 'Enable tidb_enable_lazy_cursor_fetch for a protocol cursor. Open a numeric-TS AS OF query over a 64-Region table with fetchSize=1 and hold it. Observe Sleep/empty TxnStart and etcd minStartTS crossing the snapshot. On an isolated cluster, advance GCV2 to exactly that TiDB-reported value, then continue COM_STMT_FETCH.',
 'The cursor tracker must keep the actual stale read TS registered until the cursor closes, so the normal GC frontier cannot pass it.',
 'On runtime d573e284da, cursor StartTS is 0. With default DistSQL scan concurrency, snapshot 467725273248038917 remained live while TiDB reported and PD accepted 467725284651040769; first FETCH returned Error 9006.',
 'execStmtResult.TryDetach initializes cursor.State.StartTS from TxnCtx.StartTS, but autocommit stale reads store their actual snapshot in TxnCtx.StaleReadTs or SnapshotTS. After process info becomes Sleep, ReportMinStartTS consumes the zero cursor identity and omits the live snapshot.',
 'Register the effective read TS during statement-to-cursor handoff, including stale-read and session-snapshot modes. Keep the existing ordinary-cursor minStartTS test and add a stale-read handoff test.',
 'LIVE_SNAPSHOT_VS_GCV2_FRONTIER',
 'LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF',
 'lazy-cursor-registers-startts-not-stale-read-ts',
 'GitHub master 94b834d94b source; runtime d573e284da; one TiDB; lazy cursor enabled; deferred Region reads',
 1,
 'confirmed',
 NULL,
 'Owner RED and counterfactual GREEN on local 13282a8bd0; real MySQL protocol RED on d573e284da with three TiKV nodes and default DistSQL concurrency; identical root verified on GitHub master 94b834d94b; no exact upstream dedup. Severity is moderate: lazy cursor fetch is OFF by default and the result is a clear query error, not silent data corruption.');

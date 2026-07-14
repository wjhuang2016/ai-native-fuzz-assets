# New finding: expired async transaction can return an ordinary error and later commit

Status: issue-filed as [TiDB #69831](https://github.com/pingcap/tidb/issues/69831), after confirmation
by a local client-go integration RED/GREEN experiment and by real PD/TiKV on authorized testbed
`8220955`. Recorded as remote `found_bug id2250003`; current client-go `01bd8f99` was revalidated
immediately before filing.

## User-visible symptom

An async-commit transaction can return the ordinary error `txn takes too much time` even though all
of its async prewrites have succeeded. If the best-effort cleanup does not finish, a later reader can
recover those locks as committed. The application therefore sees a normal failure, may retry the
logical operation, and can later observe the first attempt as committed too.

A realistic trigger requires `tidb_enable_async_commit=ON` (it is OFF by default), a workflow
transaction older than 24 hours, and cleanup delayed beyond lock expiry. For example, TiDB A
prewrites a two-account transfer, returns the age-limit error, then loses its TiKV path or is
terminated before background rollback completes. The application's retry on TiDB B touches the same
keys, recovers the first async lock set as committed, and can then commit the transfer a second time.
This is an outcome-contract violation and can duplicate non-idempotent business work.

The consequence is high, but the natural trigger is not high-frequency: it needs both a transaction
older than `MaxTxnTimeUse` and failed/incomplete cleanup.

## Exact trigger conditions

1. Async commit is enabled and remains selected for the transaction.
2. The transaction is older than `MaxTxnTimeUse` (24 hours) at commit time.
3. Every async prewrite succeeds and the primary publishes the complete secondary list.
4. The post-prewrite age check returns `txn takes too much time` as an ordinary error.
5. Deferred cleanup is delayed beyond lock expiry or does not complete.
6. A later lock resolver, including the application's own retry on another TiDB node, checks all
   secondaries and recovers the transaction with a nonzero commit TS.

## Deterministic reproduction

The reusable test is in
`scaffolds/client-go-tests/ai_native_async_commit_expired_probe_test.go`. It wraps the normal oracle
so only the 24-hour age check is accelerated, enables the existing client cleanup-skip failpoint,
and then reads both keys through the normal lock resolver.

```bash
cp scaffolds/client-go-tests/ai_native_async_commit_expired_probe_test.go \
  /path/to/client-go/integration_tests/
cd /path/to/client-go/integration_tests
go test . \
  -run 'TestAsyncCommitFail/TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted' \
  -count=1 -v
```

The test requires both of these observations:

- `Commit` returns an error containing `txn takes too much time` and the committer reports async mode.
- A later point get resolves the locks as committed and returns both written values.

On testbed `8220955`, the same Linux test binary ran inside `sdkserver-0` against one PD and three
real TiKV nodes. The log records `commitFailedSkipCleanup`, async recovery with a nonzero
`commitTS`, and `ResolveLock` finishing with `action=commit` for both keys. The probe then removed
its two dedicated raw keys.

Evidence:

- `assets/store/logs/txn-async-commit-age-local-red.log`
- `assets/store/logs/txn-async-commit-age-local-green.log`
- `assets/store/logs/txn-async-commit-age-testbed8220955.log`

## Source proof

Current client-go commit `661db4f5f4e85d1efe3a0f189fc80c564b7b573a` has this order:

1. `txnkv/transaction/2pc.go:1861` completes `prewriteMutations`.
2. `prewrite.go:191-196` writes `UseAsyncCommit`; the primary carries the complete secondary set
   from `2pc.go:789-800`.
3. `2pc.go:1943-1947` accepts the nonzero async `minCommitTS`.
4. Only afterward, `2pc.go:1970-1975` stores `commitTS`, checks the 24-hour limit, and returns the
   ordinary age error.
5. The deferred error path at `2pc.go:1728-1733` schedules cleanup, but `2pc.go:1643-1657` shows
   cleanup can be skipped when the store is closed or the existing failpoint models unreachability.
6. `txnkv/txnlock/lock_resolver.go:1268-1282,1313-1355` treats a complete async lock set with a
   nonzero recovered commit TS as committed and resolves all keys accordingly.

The broken proof obligation is: after successful async prewrite has crossed the recovery commit
frontier, later checks must not publish an ordinary abort unless compensating rollback is proven
complete before the error is returned.

## Counterfactual fix

A minimal local counterfactual moved the async transaction-age check before `prewriteMutations` and
skipped the post-prewrite age error for async commit. Under that change, the old bug oracle failed
because both keys were absent; the adjusted safe-outcome oracle passed. The exact patch shape is in
`scaffolds/client-go-tests/ai_native_async_commit_expired_fix.patch`.

This proves the error altitude is causal. The production fix should also decide whether 1PC needs a
separate age-limit policy; this finding does not broaden its claim to 1PC.

## Pause gate

Do not enumerate more TTL values, key counts, or cleanup error strings. Reopen only for upstream fix
validation, an independently different post-proof error, or evidence that another protocol crosses
the same irreversible frontier.

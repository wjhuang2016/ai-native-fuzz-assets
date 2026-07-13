# Transaction Failpoint Inventory

This is a boundary inventory, not a target list. A failpoint becomes usable only when its location
matches the admitted P/Q/F card and the schedule can correlate it to one transaction or key batch.

| Layer | Failpoint | Altitude | Useful property | Current limit |
|---|---|---:|---|---|
| TiKV raftkv | `applied_cb_return_undetermined_err` | A2 | Callback runs after Raft apply response reaches raftkv; substitutes an undetermined error before normal response publication | Per-pod and one-shot, but not transaction-scoped; use a quiet isolated workload or add a selector |
| client-go transport | `tikvclient/rpcFailOnRecv` | A3 | Runs after `SendRequest` returns and before response processing; supports the `write` filter | Request-family filtering is broader than one transaction |
| client-go prewrite | `tikvclient/prewritePrimary` | A1/A4 | Pauses the primary batch around prewrite dispatch | Process-global unless a new selector is added |
| client-go prewrite | `tikvclient/prewriteSecondary` | A4 | Pauses secondary batch progress | Process-global unless a new selector is added |
| client-go 2PC | `tikvclient/afterPrimaryBatch` | A4 | Pauses after the primary batch boundary | Does not itself prove TiKV apply or durable status |
| client-go 2PC | `tikvclient/beforeCommit` | A4 | Delays before commit execution | A control for ordering, not an after-apply witness |
| TiKV scheduler | `before_async_apply_prewrite_finish` | A2-adjacent | Controls async-apply prewrite completion path | Requires source tracing for the selected protocol and response edge |
| TiKV scheduler | `scheduler_async_write_finish` | A2-adjacent | Marks scheduler async write completion | Generic and broad; insufficient alone for transaction identity |

TiDB's HTTP failpoint API is present only when the failpoint image starts with:

```text
GO_FAILPOINTS=github.com/pingcap/tidb/pkg/server/enableTestAPI=return
```

The API then exposes linked TiDB and client-go failpoints through `/fail/` on port 10080. TiKV's
failpoint image exposes `/fail` on port 20180. `txnlab` reaches both through temporary loopback-only
port forwards.

## Hook Admission

Add a source hook only when existing points cannot distinguish the critical edge. A valid hook
records or filters on the minimum owner identity needed by the oracle:

- startTS and primary key for commit/status ownership;
- startTS plus forUpdateTS for pessimistic lock generation;
- region/batch plus protocol mode for fallback after a nonempty prefix.

Prefer a one-shot condition and one structured witness over multiple sleeps. The witness must state
whether the request was sent, accepted, applied, returned, suppressed, or locally published. Do not
infer those states from nearby timestamps.

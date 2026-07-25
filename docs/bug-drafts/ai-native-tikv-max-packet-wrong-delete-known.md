# TiKV MaxAllowedPacket omission can turn a root error into a successful wrong DELETE

Status: independently reproduced on official nightly; known root is TiKV
[#3736](https://github.com/tikv/tikv/issues/3736).

TiDB applies `max_allowed_packet` to `CONCAT`, while TiKV does not receive or use that semantic
input. With the default 64 MB value, four legal `SPACE(16777216)` results plus one byte cross the
boundary:

```sql
CONCAT(SPACE(n),SPACE(n),SPACE(n),SPACE(n),'x') IS NOT NULL
```

For `n=16777216`, the pushed predicate is true in TiKV but false in TiDB. The returned row projects
`predicate_holds=0`. A pushed `DELETE` succeeds and removes both test rows; the root-controlled
statement returns error 1301 and preserves both rows. Three `SPACE` terms form the GREEN control.

This is a stronger consequence witness for the old root, not a new bug. Issue #3736 explicitly
states that MaxAllowedPacket is neither pushed down nor used by TiKV.

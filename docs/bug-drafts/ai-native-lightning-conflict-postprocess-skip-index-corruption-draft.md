# TiDB Lightning can report success with a corrupted unique index when post-restore checks are off

Status: confirmed on official nightly and retained in current master source; no exact upstream issue
or PR found.

## Summary

TiDB Lightning's local backend can skip configured conflict resolution when both post-restore
checksum and analyze are disabled. With duplicate unique keys in the input, Lightning reports
success while record data and the unique index contain different rows.

Queries can return different results depending on the chosen access path. `ADMIN CHECK TABLE`
immediately reports error 8223.

## Production trigger

A migration operator uses `conflict.strategy = "replace"` because source files can contain
duplicate primary or unique keys. To shorten the import window, or because validation and statistics
collection run separately, the operator disables Lightning's post-restore checksum and analyze.

The bug needs:

1. Lightning local backend;
2. `conflict.strategy = "replace"`;
3. both post-restore checksum and analyze set to `off`;
4. two source rows that collide on a primary or unique key.

It does not need concurrency, retries, a failpoint, a source patch, multiple TiDB nodes, MDL off, an
unusual TiDB variable, or an infrastructure fault.

## Environment

```text
TiDB nightly:      ed2376acc6e0feeff9f3e2c38db489727933aa80
TiKV nightly:      730be34f959185c934b7d3db730ca1dbeb3949f8
PD nightly:        f7db42521223b92fa30d68352b15e6962b699b7e
Lightning nightly: a942e4684f4346e8f3b1b4985dfbba38ae6305e2
TiDB master:       05b396fb6636f73b3bc06b09107cf43f2c725c35
Topology:          one TiDB, one PD, one real TiKV
MDL:               enabled
sql_mode:          default strict mode
```

## Minimal reproduction

Create `data/repro-schema-create.sql`:

```sql
CREATE DATABASE repro;
```

Create `data/repro.t-schema.sql`:

```sql
CREATE TABLE t (
  id INT PRIMARY KEY CLUSTERED,
  u INT NOT NULL,
  UNIQUE KEY uu(u)
);
```

Create `data/repro.t.0.csv`:

```csv
id,u
1,7
2,7
```

Use this Lightning configuration:

```toml
[lightning]
region-concurrency = 1

[tidb]
host = "127.0.0.1"
port = 4000
user = "root"
status-port = 10080
pd-addr = "127.0.0.1:2379"

[tikv-importer]
backend = "local"
sorted-kv-dir = "/tmp/lightning-repro-sorted"
add-index-by-sql = false

[mydumper]
data-source-dir = "data"

[mydumper.csv]
header = true

[conflict]
strategy = "replace"

[checkpoint]
enable = false

[post-restore]
checksum = "off"
analyze = "off"
```

Run Lightning. It exits successfully. Then run:

```sql
USE repro;

SELECT GROUP_CONCAT(CONCAT(id, ':', u) ORDER BY id)
FROM t IGNORE INDEX(uu);
-- 1:7,2:7

SELECT GROUP_CONCAT(CONCAT(id, ':', u) ORDER BY id)
FROM t FORCE INDEX(uu);
-- 1:7

ADMIN CHECK TABLE t;
-- ERROR 8223: data inconsistency in table: t, index: uu, handle: 2 ...
```

The exact surviving unique-index owner can depend on import ordering. The invariant is stable:
the record scan contains both rows, the unique-index scan contains one, and `ADMIN CHECK TABLE`
fails.

## Matched GREEN

Change only:

```toml
[post-restore]
checksum = "required"
analyze = "off"
```

Lightning detects two conflict records, runs duplicate resolution, and retains one consistent row.
The record and unique-index scans match and `ADMIN CHECK TABLE` passes.

The self-contained scaffold reproduced the RED and matched GREEN three consecutive times.

## Root cause

`lightning/pkg/importer/table_import.go` returns immediately when checksum and analyze are both off:

```go
if rc.cfg.PostRestore.Checksum == config.OpLevelOff &&
   rc.cfg.PostRestore.Analyze == config.OpLevelOff {
    // save AnalyzeSkipped
    return
}
```

Local duplicate collection and `ResolveDuplicateRows` are later in the same function, inside the
checksum stage. The early return assumes that disabling two optional reporting stages means no
post-processing remains. That assumption is false when a conflict strategy independently requires
duplicate resolution.

## Counterfactual

A temporary current-master build changed only the early-return admission so local conflict work
still runs when the strategy is not `NoneOnDup`. The original RED configuration then detected two
conflicts, resolved them, skipped checksum and analyze as requested, and passed record/index parity
plus `ADMIN CHECK TABLE`.

The temporary source change was removed after validation.

## Expected behavior

Duplicate detection and conflict resolution must be independent of checksum and analyze. Lightning
may report success only after the configured conflict strategy has left record and index data
physically consistent.

## Impact

The import tool reports success while publishing an immediately corrupted unique index. Full scans
and indexed queries can disagree, and later reads or DML can operate on an incomplete access path.
The trigger is a plausible production migration configuration and requires only one duplicate
business key in the source data.

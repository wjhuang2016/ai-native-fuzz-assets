# NextGen IMPORT INTO write-fence probe: strong RED, known root

## Why this target was selected

The S65 selector asks for three properties:

1. a physical writer uses an older timestamp;
2. its target remains writable by ordinary SQL;
3. the physical write bypasses row/index maintenance performed by SQL.

NextGen `IMPORT INTO` matched all three in current source:

- only the Classic path enters `TableModeImport`;
- the ingest TS is allocated before `ImportEngine`;
- CSE/TiKV later ingests physical record and index KVs at that TS.

## Small matrix

The source contains one row:

```text
(id=1, u=100, payload=import)
```

The table has `PRIMARY KEY(id)` and `UNIQUE KEY(u)`. The RED pauses immediately before
`ImportEngine`, commits ordinary SQL `(1,900,'application')`, and resumes ingest. The matched
control follows the same path without the SQL write.

## Strong oracle

The probe checks the public terminal state, table scan, both unique-index predicates,
`ADMIN CHECK TABLE`, and raw MVCC timestamps.

Both RED runs show:

- the application commit TS is later than the import TS;
- the failed import leaves `(1,100,'import')` visible;
- forced lookup by stale `u=900` returns that same row;
- `ADMIN CHECK TABLE` reports error 8223.

The control finishes and remains consistent. The checksum mismatch is therefore a late detector;
it does not roll back the already ingested KVs.

## Novelty decision

The execution proves a critical data-integrity consequence, but the root is already owned by
[pingcap/tidb#69182](https://github.com/pingcap/tidb/issues/69182). That issue explicitly requires
NextGen `IMPORT INTO` to enter `Import` table mode so user writes and DDL cannot overlap a long
import.

This candidate is recorded as `DUPLICATE_KNOWN_ROOT / NOT_ADMITTED`: no new bug ID and no root-count
increment. The evidence can still be used to correct the issue's severity and validate a future fix.

## Method improvements

1. Run post-RED dedup against the root and the fix boundary, not only the observed symptom.
2. Keep strong RED evidence even when novelty fails; it can upgrade a vague enhancement into an
   execution-backed safety requirement.
3. A deterministic hook must prove it fired before the concurrent action. Direct `go test` does not
   rewrite marker-style failpoints, so use a runtime hook or enable the rewrite toolchain.
4. A terminal checksum error is not an atomicity oracle. Always inspect durable record/index owners
   after the operation fails.

## Evidence

- `assets/store/logs/nextgen-import-concurrent-dml-red1-20260725.log`
- `assets/store/logs/nextgen-import-concurrent-dml-red2-20260725.log`
- `assets/store/logs/nextgen-import-concurrent-dml-green-20260725.log`
- `assets/store/nextgen-import-concurrent-dml-known-gap-results-20260725.jsonl`

# 1PC can commit with an obsolete schema after DDL and corrupt data/index state

Status: confirmed high-severity candidate on exact TiDB commit `5c9198e9484d` and real TiKV in
testbed `8220955`. Remote bug DB: `found_bug id2280003`.

## User-visible behavior

With `tidb_enable_metadata_lock=OFF`, a concurrent autocommit INSERT can return success in 1PC mode
after `ADD INDEX` has already finished. The row exists in the table but is absent from the newly
published index:

```text
txn_commit_mode=1pc
commit_ts > ddl_finished_ts
table scan:       1:10
FORCE INDEX:      <empty>
ADMIN CHECK TABLE: error
```

The `TRUNCATE TABLE` sibling is more direct: the INSERT returns success with a commit timestamp
later than the DDL completion timestamp, but its row is not visible in the replacement table.

## Trigger conditions

1. Metadata lock is disabled, so TiDB relies on delta schema validation at commit.
2. The DML is eligible for 1PC; a single-row autocommit INSERT is sufficient.
3. A related DDL finishes after `calculateMaxCommitTS` validates the old schema and before the
   1PC prewrite reaches TiKV.
4. TiKV selects a 1PC commit timestamp later than the DDL `FinishedTS`.

The deterministic probe pauses at the existing `tikvclient/beforePrewrite` point. In production,
the same interval can be widened by RPC latency, retry/backoff, overload, or Region/leader movement.

## Reproduction

Start an exact-commit failpoint-enabled TiDB front connected to the target PD/TiKV, enable its test
HTTP API, then run:

```bash
ENABLE_ASYNC_COMMIT=1 \
MYSQL_PORT=4005 STATUS_URL=http://127.0.0.1:10085 \
bash scaffolds/top-level/ai_native_onepc_schema_horizon_probe.sh 1pc add-index
```

Expected output includes `commit_ts > ddl_finished_ts`, `current_rows=1:10`,
`index_rows=<empty>`, `admin_check_rc=1`, and `verdict=RED`.

The safe-path control changes only the protocol:

```bash
MYSQL_PORT=4005 STATUS_URL=http://127.0.0.1:10085 \
bash scaffolds/top-level/ai_native_onepc_schema_horizon_probe.sh 2pc add-index
```

It returns `txn_commit_mode=2pc`, matching table/index rowsets, `admin_check_rc=0`, and
`verdict=GREEN`.

## Root cause

TiDB installs a `SchemaChecker` before commit when MDL is off. client-go invokes it from
`calculateMaxCommitTS`, then reaches `beforePrewrite`. TiKV's successful 1PC prewrite directly
creates committed writes. client-go returns immediately from the 1PC branch and does not run the
commitTS-based schema check used by ordinary 2PC.

The checked proposition is only "the schema was valid at approximate currentTS before prewrite".
The fast path consumes a stronger proposition: "the schema is valid at the atomic 1PC apply
timestamp." The interval between those two points is uncovered.

## Fix direction

The conservative fix is to set `Enable1PC=false` whenever `needCheckSchemaByDelta` is true. The same
test then uses the existing 2PC schema check and automatic retry path, and both table identity and
index consistency oracles pass. This counterfactual changes only fast-path eligibility.

## Dedup boundary

- TiDB issue `#24009` records an unstable, skipped 1PC schema-change test and was closed as having
  no production impact. It does not document commitTS-ordered false success or persistent
  corruption on current source.
- Existing asset `id1440001` covers async commit returning `ErrInfoSchemaChanged` under MDL-off
  `ADD INDEX`. That is a false abort; this root is 1PC false success with inconsistent durable state.

# Issue62531 Row-Image Bridge Case

## Proof Obligation

`MODIFY COLUMN` creates a changing column while the old column remains readable. A DELETE plan uses `DeletableCols`, so a delete row must contain the handle, the old public value, and the changing value derived from its dependency column.

The downstream table-scan decoder must therefore preserve this implication:

```text
missing changing-column value
        => cast(dependency-column value, changing-column type)
```

It must not silently replace the value with the changing column's declared `DefaultVal`.

## Source Evidence

The narrow local instrumentation passed with this observed shape:

```text
DELETE child columns: [1, 2, 5]
data before downstream fallback: [1, 1, ""]
public columns=4, writable columns=5, deletable columns=5
deletable index layouts: old index [1], changing index [2]
table-scan fallback: column 5, DefaultVal=""
counterfactual fallback: changing value="1"
```

The counterfactual value is the cast of the dependency value `1`; the protocol-level fallback only knows `DefaultVal` and does not carry the dependency relationship.

## Live Lift

On testbed `8220955`, a current failpoint-enabled owner ran the nonpartition shape:

```text
MODIFY COLUMN val0 int -> varchar(16) NOT NULL
secondary index val0_idx(val0)
120000-row prefill
16 combined insert/delete workers
beforeUpdateColumnBackfillApply pause
```

After release, a normal prepared DELETE returned:

```text
[components/tidb_query_executors/src/table_scan_executor.rs:467]
Data is corrupted, missing data for NOT NULL column (offset = 2)
```

The DDL still reached `synced/public`. The aftermath oracle was green: `ADMIN CHECK TABLE` passed, table and index counts were both `2082`, and the formula oracle was zero. This is a user-visible execution failure during the online DDL window, but not evidence of persistent post-DDL corruption.

## Default-Value Sibling

The probe was extended with an explicit `val0 DEFAULT 7` option. Both a broad run and a single-value run were checked:

- the broad `DEFAULT 7` run finished green with equal table/index counts and no formula mismatch;
- the single-value domain (`val0=0`, changing-column default `7`) reproduced the same transient missing-NOT-NULL error, but finished with `3000` rows on both table and index paths, `ADMIN CHECK TABLE` green, and no rows at `val0=7`.

This sibling does not establish a new wrong-index root. It is a negative boundary against the stronger hypothesis that a nonmatching declared default alone leaves a persistent stale index.

## Method Lessons

1. A local counterfactual at the exact information-loss boundary is stronger than a broad source suspicion, but it is not a live root-cause proof by itself.
2. Cross-layer targets need an information-preservation check: compare the row shape before the protocol boundary with the fields the downstream decoder can actually receive.
3. A live red must be followed by an aftermath oracle. A transient `Data is corrupted` error and a durable wrong index are different findings.
4. A sibling that changes the declared default but preserves the same live error belongs to the existing family unless the fix locus or durable consequence changes.
5. A missing failpoint in the binary is `INVALID`; HTTP control availability alone is not proof that the target path is executable.

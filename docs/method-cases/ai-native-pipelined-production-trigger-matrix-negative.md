# Pipelined DML production-trigger matrix: a strong negative

## Question

Can an autocommit BULK DML leave a durable prefix after earlier generations were prewritten to
TiKV and a later row or concurrent transaction makes the statement fail?

This was admitted because the production trigger is concrete. A production import can naturally
exceed the Pipelined MemDB gate of 16 MiB and 10,000 mutation keys. A bad suffix row or an online
writer committing a suffix key can then arrive after an earlier generation was prewritten. The test
only reduced the flush thresholds and selected the second flush boundary; it did not fabricate the
NOT NULL error, duplicate, or write conflict.

## Seven-field production card

- `production_workload`: autocommit `tidb_dml_type=BULK` INSERT, UPDATE, REPLACE, or upsert over a
  large ordered input; primary, unique, and generated-column indexes are present.
- `natural_producer`: a late NULL/duplicate in the input, or an ordinary OLTP transaction that
  commits a key in the not-yet-prewritten suffix.
- `ordering`: either `flush(prefix) < evaluate(bad row) < error`, or
  `bulk start < flush(prefix) < competitor commit < flush(conflicting suffix) < error`.
- `defaults`: MDL and autocommit remain ON. BULK is the disclosed product opt-in. The test-only
  threshold reduction compresses an input-size condition that production reaches without it.
- `topology`: current TiDB, one PD, and current nightly real TiKV; no node failure is required.
- `production_outcome`: the statement returns its real data/conflict error; a fresh session must see
  no bulk prefix, or only the independently committed competitor row; every index must pass
  `ADMIN CHECK TABLE`.
- `control`: standard DML over the same ordered stream must match BULK exactly, including generated
  columns and both unique indexes.

## Result

All cells were GREEN on real TiKV `7ecce12` with TiDB `2964713` and client-go `01bd8f9`.

1. A late NOT NULL error occurred after generation 1 had flushed 1,024 keys. Rollback status was
   resolved and the fresh row count was zero.
2. A late unique-key conflict occurred after 90 generations and 4,551 row/index mutations had been
   flushed. Fresh rows, aggregate values, and `ADMIN CHECK TABLE` all matched the pre-statement state.
3. Ordered REPLACE and ON DUPLICATE streams matched standard DML row-for-row after repeatedly moving
   primary, two unique-key, and generated-index state across generations.
4. In the concurrency cell, generation 1 flushed 2,048 keys before the competitor committed handle
   1500. Generation 2 returned error 9007; range rollback completed; a fresh session saw only the
   competitor row.

The holding guard is durable rollback status plus range/read-assisted lock cleanup. A cancelled
best-effort cleanup worker may leave temporary locks, but a later reader still resolves them as
rollback. That is availability residue, not durable data corruption.

## Method lesson

A production card changes what gets tested. Instead of injecting a generic RPC failure, it yields a
bounded matrix over ordinary data and concurrency producers. A GREEN is reusable only after the log
proves that the dangerous prefix was actually flushed before the natural failure. Reopen this family
only when a new candidate can change primary status to commit, omit a proof from recovery evidence,
or cross another irreversible owner; changing row count, error text, or key position is not enough.

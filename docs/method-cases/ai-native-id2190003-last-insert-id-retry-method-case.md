# id2190003: retry must roll back publication state even when re-entry has no consumer

Remote bug DB: `found_bug id2190003`, issue-filed high severity; upstream
[TiDB #69796](https://github.com/pingcap/tidb/issues/69796).

## Why S45 found it

The earlier S45 model ranked state that a rebuilt attempt consumes. `LAST_INSERT_ID` exposed a
missing sibling shape: failed-attempt state can survive until statement completion even when the
successful attempt does not read or overwrite it.

```text
P: KV statement state is rolled back and the executor is rebuilt
Q: only values produced by the successful attempt can be published
F: LAST_INSERT_ID(expr) mutates StatementContext before a retryable lock conflict;
   ResetForRetry omits that state; the zero-match retry leaves it untouched
consumer: statement completion publishes the survivor, and the next statement persists it
```

The method therefore needs two consumer classes:

1. **re-entry consumer**: a survivor changes a later attempt's key, predicate, row image, or action;
2. **terminal publication consumer**: re-entry omits the operation, so completion publishes a
   survivor that belongs only to the failed attempt.

## Minimal matrix

| Cell | Final visible state before successful attempt | Result |
| --- | --- | --- |
| Natural local conflict | unique key and gate committed after first evaluation | UPDATE succeeds with zero match; published/sink `99` |
| Exact reset | same conflict and schedule | published/sink `7` |
| SQL-only real TiKV | same two-session schedule with `SLEEP(20)` | zero match; published/sink `99` |
| No failed attempt | gate already exists | zero match; published/sink `7` |

The durable sink is important. Observing only the session variable would understate impact; using
the next statement as a consumer proves that hidden attempt state can become committed user data.

## Selector improvement

For every retry boundary, inventory both:

- state read during re-entry;
- state published after re-entry returns, especially fields guarded by a `Set`, `Valid`, `Dirty`,
  `Changed`, or `Present` flag.

Rank a field pair highly when the retry reset clears neighboring statement outputs but omits that
value/flag pair. Add a zero-work retry cell: make the successful attempt skip the setter entirely.
This is often more discriminating than replaying the same setter twice.

The executable selector in
`scaffolds/go-probes/retry_reset_publication_omission_scan.go` applies that rule to non-test Go
source. On current TiDB `13282a8`, it collapsed nested reset helpers and returned two candidates:
`LastInsertID/LastInsertIDSet` ranked first at 100, while `planHint/planHintSet` ranked second at 90.
The bug was already RED before this extraction, but the held-out scanner result proves the lesson is
reusable as a source selector rather than merely a post-hoc explanation.

## Reuse and novelty

Reused assets: S45 selector, pessimistic post-evaluation fault boundary, pre/post-evaluation retry
schedule, and durable row-image principle. New assets: terminal-publication obligation,
zero-match oracle, unique-key-plus-gate scenario, and executable retry-reset omission selector.

The root is distinct from `id2100003` by missing state owner and terminal consumer. SQL forms,
sleep duration, inserted ID values, and alternate gates are blast radius and must not be counted as
new bugs.

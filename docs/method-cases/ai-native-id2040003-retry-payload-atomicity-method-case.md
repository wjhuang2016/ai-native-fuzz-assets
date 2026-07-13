# id2040003: retry-attempt derived payload atomicity

Remote bug DB: `found_bug id2040003`, confirmed high.

## P / Q / fast path

- P: the retry callback returned nil and `subTaskMetas` is nonempty.
- Q: the metas cover the complete table key range exactly once.
- Fast path: publish the metas as DXF ReadIndex subtasks without comparing them with the source
  range.

P proves only that some payload exists. It does not prove completeness or attempt ownership.

## Why the method worked

The source scanner did not stop at the suspicious `return true, nil`. It tracked a mutation to a
slice declared outside the retry closure, then placed the fault after a nonempty prefix. That made a
two-batch matrix sufficient:

- current source: 2 -> 1;
- error-only fix: 2 -> 3;
- full attempt-local fix: 2 -> 2.

The second RED is the important methodology improvement. A generic "preserve the error" rule would
have proposed an incomplete fix. Tracking derived payload ownership across attempts exposed the
actual invariant.

## Reusable selector

Search for retry closures that mutate captured slices, maps, objects, cursors, or summaries. Rank a
hit when a fallible operation occurs after mutation and the caller publishes the captured state.
Inject failure after the smallest nonempty prefix and observe both error identity and payload
coverage/cardinality.

Safe alternatives are attempt-local construction, full overwrite before every fallible suffix, or
an explicit exact-coverage postcondition before publication.

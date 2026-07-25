# id3600003: a remote evaluator must not turn an exception into an ordinary value

## Starting proof obligation

```text
P: TiDB and TiKV receive the same SQL cast, return type, and evaluation flags.
Q: pushing the cast preserves its value and warning/error terminal.
F: TiKV converts JSON U64 with unchecked `as i64`, erasing overflow as a negative value.
```

The high-value difference was not merely `value A != value B`. Under strict DML, TiDB has no legal
row set because evaluation must abort. TiKV manufactures a normal row set and lets `DELETE` commit.

## Efficient discovery path

1. Query existing assets for JSON exact-domain and pushdown-DML roots.
2. Split the conversion source by JSON variant:
   `Literal | I64 | U64 | Double | String | non-scalar`.
3. Compare each branch's intermediate type and error handling across TiDB and TiKV.
4. Notice that only the TiKV U64 branch uses an unchecked narrowing cast.
5. Derive the exact boundary `MaxInt64 + 1`, rather than fuzzing arbitrary JSON.
6. Prove a pushed/root row-set difference, then immediately lift it to strict `DELETE`.

This reused id3480003's exact-domain matrix and id3330003's persistent pushdown oracle. The new
asset is the terminal mismatch: an error state is converted into a valid remote value.

## Small matrix

| Cell | TiDB root | TiKV pushdown | Verdict |
| --- | --- | --- | --- |
| JSON INTEGER `MaxInt64` | value | same value | GREEN |
| JSON U64 `MaxInt64+1` in SELECT | capped value + 1690 warning | negative value | RED |
| same U64 in strict DELETE | error 1690, 0 writes | success, row deleted | RED |
| U64 branch uses bounded conversion | error 1690, 0 writes | error 1690, 0 writes | GREEN |

The production-shaped row stores the U64 under `payload.account_id`; `JSON_EXTRACT` and the cast are
both pushed. Two valid external IDs disappear while the ordinary signed control remains.

## Strong oracle

1. Record exact input type with `JSON_TYPE`.
2. Prove operator ownership with `EXPLAIN`.
3. Keep expression type and predicate fixed; change only execution altitude.
4. Compare value, warning count, error code, and exact selected handles.
5. Reset matched table copies and run persistent DML.
6. Fresh-read named preimages and run `ADMIN CHECK TABLE` only as a secondary check.
7. Reproduce the primitive on current source with a focused test.
8. Change only the unchecked conversion owner and require the same test to turn GREEN.

## Selector

Add `PUSHDOWN_EXCEPTION_TO_VALUE_CLOSURE`:

```text
remote expression admitted
  + root branch can overflow, truncate, reject, or return NULL
  + remote branch narrows or coerces before consulting EvalContext
  + remote ordinary value changes row membership
  + persistent consumer
  - exact typed/error conformance
```

Procedure:

1. Enumerate source variants before generating values.
2. For each branch, mark the legal terminal set: value, warning, error, or NULL.
3. Search remote implementations for unchecked casts, lossy intermediates, default context, and
   ignored errors.
4. Derive the first boundary that leaves the destination domain.
5. Run warning and strict cells separately.
6. Lift any exception-to-value transition directly into `DELETE`, `UPDATE`, or materialization.
7. Prove a one-owner counterfactual before widening the matrix.

## Why it worked

Pure SQL fuzzing has a huge JSON and expression space. Source partitioning reduced it to one suspect
branch and four boundary values. The strict-DML cell then supplied a stronger oracle than result
comparison: one engine must abort, while the other reports success and changes durable data.

The method also avoided overcounting related roots. TiDB #57848 uses JSON-versus-DOUBLE equality.
id3480003 narrows JSON integers through `f64` when converting to DECIMAL. This root loses U64
overflow through `as i64` when converting to SIGNED.

## Cross-module transfer

- TiFlash and MPP expression evaluators;
- vectorized and scalar execution paths;
- storage codecs and coprocessor aggregation;
- generated values and materialized summaries;
- import, restore, and data-transformation evaluators;
- optimizer rewrites that move error-producing expressions across execution boundaries.

## Stop rule

Values inside the same U64-to-SIGNED branch, JSON path spellings, and DML verbs are blast radius.
Reopen only for a different source variant, destination type, evaluator, error policy, or higher
persistent consumer.

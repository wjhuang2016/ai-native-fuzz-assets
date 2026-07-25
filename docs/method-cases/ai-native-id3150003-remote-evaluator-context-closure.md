# id3150003: remote evaluators need the full semantic context

Status: confirmed high severity with direct silent row deletion.

## Proof obligation

```text
P: local and remote sides implement CastJsonAsString.
Q: the remote implementation receives every parameter that can change value, warning, or error.
F: target flen is omitted; TiKV compares full JSON text and supplies the wrong handles to DELETE.
```

Function-name parity proves much less than semantic parity. The proof domain includes:

- input type flags;
- return length and scale;
- charset and collation;
- padding behavior;
- sql_mode and statement error policy;
- timezone and other session context.

## Small matrix

Hold the JSON value fixed and vary only the return length:

| JSON | `CHAR(n)` | TiKV | TiDB | DML consequence |
| --- | --- | --- | --- | --- |
| `12` | 4 | `12` | `12` | GREEN |
| `1234` | 4 | `1234` | `1234` | GREEN |
| `1234.5` | 4 | `1234.5` | `1234` + truncation | RED |
| `1234.5` | 8 | `1234.5` | `1234.5` | GREEN |

Default strict DML sharpens the RED: TiKV deletes, while TiDB raises 1406 before mutation.

## Strong oracle

1. `EXPLAIN` proves remote row admission.
2. Push/root queries compare ordered primary keys.
3. Projecting the cast and predicate onto the result exposes `predicate_holds=0`.
4. Pushed `DELETE` records deleted and surviving IDs.
5. Root-controlled `DELETE` records error 1406 and unchanged preimages.
6. Current-master assertion proves the owning function still lacks the parameter.

## Selector

Use `REMOTE_EVALUATOR_CONTEXT_CLOSURE`:

```text
candidate = local/remote evaluator pair
            intersect local use of return type or session context
            intersect remote function capture list missing that input
            intersect boundary where the missing input changes row or error semantics
            intersect persistent remote row-set consumer
```

The source shortcut is structural: compare the local evaluator's inputs with the remote RPN
function's `capture=[...]` list before generating data.

## Loop improvement

For every pushed signature:

1. enumerate all semantic inputs used by TiDB;
2. enumerate protobuf fields and remote captures;
3. diff the two input sets;
4. vary one missing dimension while holding value and plan fixed;
5. compare value, warning/error, and exact row set separately;
6. lift row/error differences through strict DML;
7. preserve exact-length and under-length controls;
8. confirm the missing input in current owner code;
9. stop at the highest consumer for that omitted context.

This turns a large expression fuzzing space into a parameter-closure audit.

## Stop rule

All overlong JSON shapes and all bounded string predicates share this root. Reopen only when
another scalar signature omits a different context parameter, or another consumer bypasses a
separate admission proof.

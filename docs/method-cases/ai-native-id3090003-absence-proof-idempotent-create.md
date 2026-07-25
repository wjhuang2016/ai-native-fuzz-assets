# id3090003: an absence check does not identify a later idempotent create

Status: confirmed high severity / critical data-integrity consequence on official BR and real
TiKV.

## Proof obligation

```text
P: name N was absent at the safety precheck.
Q: the object later resolved as N is the object this operation intended to create.
F: another actor creates N; idempotent create suppresses the conflict; weak-name lookup asserts Q.
```

The dangerous sequence is:

```text
check absent
gap
create IF NOT EXISTS / OnExistIgnore
lookup by name
perform irreversible work against the looked-up identity
```

## Small matrix

| Competing CREATE | Position | Result | Verdict |
| --- | --- | --- | --- |
| incompatible `uk(b)` | before precheck | restore fails before ingest | GREEN reference |
| none | n/a | original `uk(a)`, ADMIN passes | GREEN |
| incompatible `uk(b)` | after precheck | restore success, wrong index | RED |
| same RED rerun | after precheck | same wrong point lookup | RED |

The concurrent action is ordinary DDL. The default checkpoint path provides the timing observer, so
no product failpoint or process pause is needed.

## Strong oracle

Join:

1. restore exit, terminal summary, and checksum;
2. post-restore schema fingerprint;
3. primary-record rows;
4. forced-index point queries with predicate self-check;
5. structural validation;
6. the first mutating DML consumer.

The key witness is:

```text
planned predicate: b=10 through uk(b)
returned row:      b=100
self predicate:    false
UPDATE result:     one wrong row modified successfully
```

This proves persistent wrong-result and wrong-write behavior after a successful recovery.

## Selector

```text
candidate = safety precheck of absence or compatibility
            intersect non-atomic create
            intersect duplicate suppression
            intersect reacquisition by mutable name or path
            intersect partial or missing semantic fingerprint validation
            intersect irreversible write, mapping, cleanup, or publication
            minus atomic claim or exact identity token carried to the consumer
```

Use the name `CHECK_CREATE_USE_IDENTITY_CLOSURE`.

## Why this worked

The first BR bug in this round asked whether a selected object set closed its reference graph. This
one asks whether a checked name still denotes the same object at consumption time.

The source looked defensive because it had an explicit existence precheck. Following the proof to
the next owner exposed the contradiction: the later create intentionally ignores existence. A
second weak step then reacquires the winner by name and checks only one schema bit.

The high-yield AI question is:

> What exact identity token produced by the successful create reaches the irreversible consumer?

If the answer is "we look it up by name again," the precheck has not proved ownership.

## Loop improvement

For every check-then-create workflow:

1. identify the state read by the safety precheck;
2. find the actual uniqueness or ownership claim;
3. inspect `IF NOT EXISTS`, ignore, upsert, retry, and resume semantics;
4. record whether create returns a stable identity token;
5. follow every later name/path lookup;
6. compare full semantic fingerprints, not one compatibility bit;
7. substitute a different object inside the gap;
8. lift through the highest name-mapped physical consumer;
9. keep preexisting and no-competitor controls.

This transfers to restore/import targets, metadata bootstrap, cache population, job registration,
object-store manifests, and cleanup queues.

## Stop rule

One incompatible index mapping already proves the identity gap. Different columns, types, defaults,
constraints, table options, and index counts are blast radius. Reopen only for another create/claim
primitive or a distinct irreversible consumer.

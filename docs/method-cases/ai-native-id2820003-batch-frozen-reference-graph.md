# id2820003: batch operations must evolve their lookup graph

Status: confirmed high-severity data-integrity root on current TiDB master and real TiKV.

## Proof obligation

```text
P: the name-indexed reference graph is valid before a multi-object operation starts.
Q: every member can use that frozen graph while earlier members mutate names and edges.
F: a later member consumes an intermediate name created earlier in the same batch.
```

The batch invariant is:

```text
For every dependency edge A -> B, the edge must follow B's object identity through
the complete permutation, including names created and reused inside the statement.
```

## Small matrix

| Rename shape | FK parent moves | Intermediate name reused | Verdict |
| --- | ---: | ---: | --- |
| `parent_old -> parent_new` | yes | no | GREEN |
| disjoint multi-rename | yes | no | GREEN, existing coverage |
| `p1 -> tmp -> p2`, then `p3 -> tmp` | yes | yes | RED |
| same reused-name shape with evolving loaded-edge lookup | yes | yes | GREEN counterfactual |

The third row is the minimum useful permutation. Two independent renames cannot expose the gap.

## Selector

Store this as `COLLECTION_SNAPSHOT_MUTATION_GAP`:

```text
candidate = operation iterates over a batch
            intersect each member queries a snapshot/cache/name index
            intersect an earlier member can mutate keys or edges used by later lookup
            intersect a later member can reuse an intermediate key
            intersect irreversible metadata/data/publication consumer
            minus evolving graph, object-ID mapping, batch revalidation, or fail-closed behavior
```

Search for loops whose body reads a snapshot obtained before the loop. Then classify every lookup
key as stable object identity, original name, current name, or generated intermediate name.

## Strong oracle

Use four layers:

1. `identity`: the dependency points to the final name of the original object.
2. `old data`: rows valid before the operation remain valid afterward.
3. `bidirectional enforcement`: the correct parent blocks delete and the wrong parent cannot
   authorize insert.
4. `blind-spot check`: run ordinary consistency checks and record whether they detect the semantic
   rebind.

Metadata text alone is too weak. The two DML directions prove that runtime enforcement consumes the
wrong edge, while the pre-existing row proves immediate persistent damage.

## Why this worked

The source already contained the proof boundary: one `InfoSchema` snapshot was captured outside a
loop, while a shared helper accumulated mutations inside the loop. A name reuse permutation made
the snapshot disagree with the helper's evolving state. The selector reduced the search to one
high-information matrix rather than enumerating `RENAME TABLE` syntax.

## Improvement to the loop

Add a `self-mutating lookup source` pass after identifying a batch owner:

1. list the lookup sources captured before iteration;
2. list the keys and edges each member can create, move, or delete;
3. generate a three- or four-step permutation where a generated key is consumed later;
4. compare final object identity, old-data validity, and both DML enforcement directions;
5. pause on RED and back-solve the snapshot/graph owner.

Apply this pass across DDL, privilege batches, placement/rule updates, backup manifests, task
schedulers, cache invalidation, and multi-key transaction helpers. The selector is collection-wide
and is not tied to the transaction module.

## Stop rule

Count one root per frozen graph / evolving batch / highest consumer tuple. More table names, longer
cycles, cross-schema variants, and alternative FK actions are blast radius unless they expose a
different lookup owner or a higher consequence.


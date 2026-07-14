# id2550003: recovery certificates must cover proof dependencies

Remote bug DB: `found_bug id2550003`, issue-filed high severity / critical consequence;
upstream: [TiDB #69832](https://github.com/pingcap/tidb/issues/69832).

## Missed proof obligation

The previous async-secondary pass asked whether every **accepted write key** appeared in recovery
ownership. That closed negative, but the vocabulary was too narrow. A transaction outcome also depends on
proof-only mutations that write no lock and therefore cannot appear in the accepted-lock set.

```text
P: primary async lock plus listed secondaries are sufficient to recover commit.
Q: every prerequisite for committing the logical transaction succeeded.
F: CheckNotExists is a prerequisite, can fail in another Region, and is excluded from the certificate.
```

## Improved selector

Add `RECOVERY_CERTIFICATE_PROOF_CLOSURE`:

1. Enumerate effect mutations and proof mutations separately.
2. For each proof, identify its failure result and whether it writes durable success/failure evidence.
3. Build the recovery certificate from all commit prerequisites, not only keys that may hold locks.
4. Split one effect owner and one proof owner across batches/Regions.
5. Let the effect prefix succeed and the proof fail naturally.
6. Remove only compensation, then invoke an independent recovery owner.
7. Compare the public terminal result with fresh durable state.

The strong oracle is:

```text
definite constraint failure => no effect mutation may later become committed
```

## Why the new method worked

The key move was changing the set equation from:

```text
accepted async lock keys - recovery members
```

to:

```text
all logical commit prerequisites - durable recovery evidence
```

`CheckNotExists` disappeared from the first equation by design, so the earlier scan considered it safe.
It is immediately visible in the second equation because `AlreadyExist` invalidates the whole transaction.

## Production compression

The test injects only cleanup loss and lock expiry. Duplicate-key failure, async primary prewrite,
cross-Region batching, terminal error, lock recovery, and durable account read all use real TiDB/client-go/
TiKV behavior. A second run removed Region-delay injection and reproduced 3/3, proving ordinary parallel
prewrite supplies the necessary ordering.

## Generalization

Apply the selector to proof-only operations such as uniqueness checks, FK existence proofs, assertions,
schema/version predicates, conditional writes, and protocol mode eligibility. Any proof omitted from a
recovery/checkpoint certificate needs one of three guards: synchronous completion before effect
publication, durable proof evidence consumed by recovery, or protocol fallback that cannot independently
commit the effect prefix.

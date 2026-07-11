# Method Case: id30036 AVG decimal scale after `div_precision_increment`

## What We Were Testing

id30036 tests whether the id30035 selector generalizes beyond scalar constant folding.

The improved S7 rule is:

```text
cached payload must be a pure function of explicit SQL inputs,
cache-key dimensions, and hidden session/config inputs consumed during build,
rewrite, folding, type inference, or boundary setup.
```

The proof card:

```text
P_check:  prepared plan-cache key says the cached plan can be reused
Q_claim:  cached AVG return scale remains valid after session precision changes
F_effect: cache hit reuses aggregate descriptor / RetTp.Decimal inferred under old precision
D_dim:    AVG type inference reads @@div_precision_increment
```

## Small Matrix

```text
direct dpi=4: AVG(x), CAST(AVG(x) AS CHAR) -> 1.5000,     1.5000
direct dpi=8: AVG(x), CAST(AVG(x) AS CHAR) -> 1.50000000, 1.50000000

prepare under dpi=4, SELECT AVG(x) -> 1.5000, cache=0
switch dpi=8, same EXECUTE -> 1.5000, cache=1 RED
direct under dpi=8 -> 1.50000000
flush session plan cache, same EXECUTE -> 1.50000000, cache=0 GREEN reference

derived COUNT form:
dpi=4, CAST(AVG(x) AS CHAR)='1.50000000' -> 0
dpi=8 cache hit -> 0 RED
dpi=8 direct -> 1
dpi=8 after flush -> 1
```

## Why This Was Fast

The selector was already narrowed by id30034 and id30035:

```text
hidden context getter
not represented in plan-cache key
consumed while building cached payload
strong direct/cache-hit/flush oracle
```

Instead of enumerating arithmetic syntax, we scanned `GetDivPrecisionIncrement()` consumers. The
aggregate path stood out because `typeInfer4Avg` reads the getter and writes durable return metadata.
That gave a small direct contract before any fuzzing:

```text
AVG(DECIMAL(10,0)) under dpi=4 -> 1.5000
AVG(DECIMAL(10,0)) under dpi=8 -> 1.50000000
```

Once the direct contract existed, the cache matrix was only four cells: first execute, switch,
cache-hit execute, flush reference.

## Selector Improvement

After id30036, the high-yield S7 unit is not a function name. It is a tuple:

```text
getter -> consumer -> cached payload class -> oracle
```

Payload classes now observed:

- **folded scalar value**: `WEEK(date)` / `1/7` become cached constants.
- **semantic tree or boundary**: `sysdate()` rewrite and timezone-folded timestamp literals.
- **type/descriptor metadata**: `AVG()` keeps `RetTp.Decimal` inferred under old precision.

This explains why some siblings are green. `GROUP_CONCAT` under `group_concat_max_len` hit plan
cache but returned current-session output, so the relevant boundary was rebuilt at execution. AES
under `block_encryption_mode` either errored with current semantics or did not hit cache in the
semantic-changing 3-argument case.

## Next Search Rule

For every `EvalContext` / `BuildContext` getter:

1. Subtract variables already covered by cache key or explicit SQL arguments.
2. Find consumers that write durable cached payloads: constants, scalar-function trees, aggregate
   descriptors, return types, ranges, or request boundaries.
3. Build a two-value direct contract before touching cache.
4. Run only the red-cell matrix: old prepare, new execute, direct reference, flush reference.
5. Stop after one representative hit per hidden-input/payload class unless a new user consequence
   or fix-validation need appears.

# id630012 Method Case: Type Equality Is Not FK Signedness Compatibility

## Selector

```text
S16_DDL_VALIDATOR_ORDERING_GAP
```

This is the second confirmed hit from S16, but it teaches a sharper sub-rule:

```text
P_check:  FK MODIFY validator sees unchanged type, flen, and decimal, then returns nil
Q_claim:  the modified FK column remains compatible with the related FK column
effect:   INT -> INT UNSIGNED keeps the coarse type tuple equal but changes signedness
D_dim:    signedness is part of FK compatibility and part of cascade write safety
```

The proof bug is not generic FK syntax. It is a validator using a coarse equality predicate as proof
of a richer target-state invariant.

## Matrix

| Cell | SQL shape | Oracle | Result |
| --- | --- | --- | --- |
| Direct target | parent `INT`, child `INT UNSIGNED`, FK | target-state validator rejects | GREEN, ERROR 3780 |
| Valid control | parent `INT`, child `INT`, `ON UPDATE CASCADE` | update parent `1 -> -1` cascades | GREEN, both rows become `-1` |
| Transition red | valid signed/signed FK, then child `MODIFY a INT UNSIGNED` | should match direct target rejection | RED, ALTER succeeds |
| Runtime consequence | parent update `1 -> -1` after red ALTER | cascade should not reach invalid schema | RED, ERROR 1264 |
| Round-trip revalidation | drop FK, add same FK again | same final metadata should reject | GREEN, ERROR 3780 |
| Collation sibling | child FK column collation change | same S16-looking dimension | GREEN, later indexed-column collation validator blocks |
| PK NULL sibling | primary-key column `MODIFY ... NULL` | same ordering suspicion | GREEN, later primary-key/default checks block |

## Oracle

```text
O19_TARGET_STATE_REJECTION_REFERENCE
```

The oracle is stronger than "ALTER succeeded":

```text
direct target state is rejected by CREATE/ADD FK
transition path reaches the same FK metadata
behavior control shows the valid schema can perform the cascade
red schema fails when the omitted dimension is exercised
round-trip ADD FK rejects after DROP FOREIGN KEY
```

For id630012, the behavior oracle is `ON UPDATE CASCADE` from `1` to `-1`. A signed child can store
the cascaded value. The unsigned child created by the transition cannot.

## Why The Method Worked

After id630011, the obvious but low-value move would have been to enumerate FK actions. The better
move was to inspect the exact proof predicate:

```text
newCol.GetType() == originalCol.GetType()
newCol.GetFlen() == originalCol.GetFlen()
newCol.GetDecimal() == originalCol.GetDecimal()
```

Then compare it with the stronger CREATE/ADD FK predicate:

```text
type
unsigned flag
charset
collation
referential-action nullability
```

That immediately produced a small matrix:

```text
unsigned flag -> likely red because no later FK validator covers it
collation     -> must be tested but likely green because indexed-column checks may cover it
nullability   -> already red in id630011
```

This is why the method was fast: it did not ask "what FK combinations exist?" It asked "which
dimensions are omitted from P_check but required by Q_claim, and which omitted dimensions are not
covered by a later safe path?"

## Quality

Medium severity, high method value.

- The DDL accepts a schema that TiDB's direct FK validator rejects.
- The behavior consequence is user-visible: parent-side DML that cascades in the valid control
  fails with `ERROR 1264` after the red transition.
- The result is fail-stop, not silent corruption in the observed repro.
- The negative siblings are as useful as the red cell: primary-key NULL and collation both looked
  suspicious statically but were covered by later validators.

## Selector Refinement

S16 should now be stated as:

```text
code checked P_coarse
system therefore believed Q_rich
then the DDL path skipped the rich target-state validator
```

The audit must include a coverage pass:

```text
for each omitted D dimension:
  is there a later validator that checks it on the complete target state?
  if yes -> likely GREEN calibration
  if no  -> build the smallest direct-target vs transition matrix
```

This refinement explains all three same-turn outcomes:

- FK nullability: red, not covered later.
- FK signedness: red, not covered later.
- FK collation: green, covered later by indexed-column collation validation.

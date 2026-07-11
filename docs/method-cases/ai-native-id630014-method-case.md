# id630014 Method Case: ID-Swap DDL Must Re-Prove Side-State Ownership

## What Worked

This bug did not come from expanding the old masking-policy matrix. The old matrix was useful
because it showed the basic owner was mostly well handled: rename rewrites names, column changes
rewrite column bindings, drop cleans rows, and truncate remaps `table_id`.

The new move was to ask a narrower question:

```text
If one DDL path has explicit owner repair helpers,
is there a sibling DDL path that changes the same ownership dimension but bypasses those helpers?
```

`EXCHANGE PARTITION` is exactly that sibling path. It swaps the standalone table ID with a partition
physical ID, while masking policies are stored by `table_id`/`column_id` and exposed through logical
table names.

## Audit Card

```text
Target:      EXCHANGE PARTITION x masking-policy side metadata

P_check:     checkExchangePartition validates table shape and rejects a few known unsupported
             owner states such as standalone-table foreign keys.

Q_claim:     after EXCHANGE PARTITION, all side metadata attached to either exchanged object still
             resolves to a coherent logical owner, or the DDL was rejected before the swap.

D_dims:      object ID vs owner name, table ID vs partition physical ID, side rows keyed by ID but
             managed by logical table DDL, and sibling repair helpers for rename/truncate.

F_effect:    onExchangeTablePartition swaps partDef.ID and nt.ID. Later masking-policy management
             DDL trusts the current logical table ID and therefore cannot find a row left on the
             old table ID.

O_oracle:    side-state owner remap oracle:
             1. policy is operable before the ID-swap DDL;
             2. after DDL, every visible policy row's owner ID resolves to its logical table;
             3. DISABLE/ENABLE/DROP/recreate affect exactly one live owner;
             4. no row remains on an old partition/table ID with a stale logical table name.

R_redflag:   a side sys table stores both logical names and physical object IDs, while an ID-swap
             DDL path does not call the owner-specific remap helper used by truncate/rename.

S_selector:  S4_ID_SWAP_OWNER_MAPPING
```

## Small Matrix

The matrix stayed deliberately tiny:

```text
control: masking policy on nt, before EXCHANGE
  ALTER TABLE nt DISABLE/ENABLE MASKING POLICY mp_nt -> GREEN

red: masking policy on nt, then EXCHANGE pt.p0 WITH nt
  policy row still says table_name=nt
  policy table_id equals pt.p0 tidb_partition_id
  ALTER TABLE nt DISABLE/DROP MASKING POLICY mp_nt -> RED, "doesn't exist"
  ALTER TABLE pt DISABLE MASKING POLICY mp_nt -> RED, "doesn't exist"

consequence: recreate same policy on nt
  creates a new row on current nt table_id
  DISABLE affects only the new row
  old row remains ENABLED on old ID
```

No broad SQL fuzzing was needed. The evidence follows from one ownership dimension: the ID used by
the side table changed owners, but the side table was not remapped.

## Why It Found a Real Bug Quickly

The high-yield selector was not "masking policy is buggy". In fact, basic masking-policy DDL had
already looked green. The efficient shape was:

```text
basic owner matrix green
+ one path has explicit repair helper
+ sibling DDL changes the same ID/name binding
+ sibling path does not mention that owner
+ management DDL gives a round-trip oracle
= high-value red cell
```

That narrows the search much more than enumerating owners or DDL syntax. It also explains why this
bug quality is better than a raw system-table mismatch: the oracle is the user's management command,
not only a metadata query.

## Stop Rule

Do not expand masking-policy syntax or every partition shape. The root family is closed enough for
fix discussion.

Reopen S4 only for:

- another ID-swap or move/rekey DDL path with a different side-state owner;
- a path where the common owner helper is green but a sibling owner-changing entrypoint bypasses it;
- fix validation for id630014;
- a new behavior oracle showing stale side state affects security/data behavior beyond management.

The reusable target is not "more masking policy". It is:

```text
side metadata keyed by object ID
+ public management surface keyed by logical owner
+ DDL swaps or moves IDs without owner-specific remap
```
